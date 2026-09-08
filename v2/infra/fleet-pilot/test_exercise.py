import io
import json
import unittest
from unittest.mock import patch

import exercise


class Response(io.BytesIO):
    status = 200


class ProbeTests(unittest.TestCase):
    allocation = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
    source = "a" * 40
    namespace = "norn-pilot-pilot260907a"

    def probe(self, payload):
        with patch("exercise.urllib.request.build_opener") as opener:
            opener.return_value.open.return_value = Response(payload)
            return exercise.request(
                "https://staging.example.test", "GET", "receipt-1",
                self.source, {self.allocation},
            )

    def test_matching_receipt(self):
        payload = json.dumps({"id": "receipt-1", "allocation": self.allocation, "version": self.source}).encode()
        self.assertTrue(self.probe(payload)["ok"])

    def test_missing_or_unrelated_provenance_fails(self):
        unrelated = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
        for value in (
            {"id": "receipt-1"},
            {"id": "receipt-1", "allocation": self.allocation},
            {"id": "receipt-1", "allocation": unrelated, "version": self.source},
            {"id": "receipt-1", "allocation": self.allocation, "version": "wrong"},
        ):
            with self.subTest(value=value):
                self.assertFalse(self.probe(json.dumps(value).encode())["ok"])

    def test_wrong_receipt_fails(self):
        self.assertFalse(self.probe(b'{"id":"receipt-2"}')["ok"])

    def test_non_object_or_malformed_json_fails(self):
        for payload in (b"[]", b"null", b"not-json"):
            with self.subTest(payload=payload):
                self.assertFalse(self.probe(payload)["ok"])

    def test_oversized_response_fails(self):
        self.assertFalse(self.probe(b" " * 65537)["ok"])

    def test_fault_rejects_short_allocation_prefix(self):
        self.assertFalse(exercise.stop_pilot_allocation("aaaaaaaa", self.namespace))

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_fault_stop_requires_target_job(self, run, _which):
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({"JobID": "another-job", "Namespace": self.namespace})
        self.assertFalse(exercise.stop_pilot_allocation("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", self.namespace))
        self.assertEqual(run.call_count, 1)

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_fault_stop_is_scoped_to_pilot_allocation(self, run, _which):
        inspect = type("Result", (), {"returncode": 0, "stdout": json.dumps({"JobID": "hello-norn-mysql", "Namespace": self.namespace})})()
        stopped = type("Result", (), {"returncode": 0, "stdout": ""})()
        run.side_effect = [inspect, stopped]
        allocation = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
        self.assertTrue(exercise.stop_pilot_allocation(allocation, self.namespace))
        self.assertEqual(run.call_args_list[0].args[0], ["nomad", "alloc", "status", "-namespace=" + self.namespace, "-json", allocation])
        self.assertEqual(run.call_args_list[1].args[0], ["nomad", "alloc", "stop", "-namespace=" + self.namespace, "-yes", allocation])

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_fault_stop_rejects_same_job_in_another_namespace(self, run, _which):
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({"JobID": "hello-norn-mysql", "Namespace": "norn-pilot-pilot260908b"})
        self.assertFalse(exercise.stop_pilot_allocation(
            "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", self.namespace
        ))
        self.assertEqual(run.call_count, 1)

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_nomad_inventory_requires_two_distinct_ingress_nodes(self, run, _which):
        allocations = ["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"]
        image = "registry.example.test/norn/hello@sha256:" + "a" * 64
        def result(value):
            return type("Result", (), {"returncode": 0, "stdout": json.dumps(value)})()
        run.side_effect = [
            result([{"ID": allocations[0], "ClientStatus": "running"}, {"ID": allocations[1], "ClientStatus": "running"}]),
            result({"JobID": "hello-norn-mysql", "ClientStatus": "running", "Namespace": self.namespace, "NodeID": "node-a", "CreateIndex": 10, "ModifyIndex": 11, "Job": {"Meta": {"pilot_image": image, "pilot_source_version": "a" * 40, "pilot_hostname": "pilot.example.test"}}}),
            result({"NodePool": "ingress"}),
            result({"JobID": "hello-norn-mysql", "ClientStatus": "running", "Namespace": self.namespace, "NodeID": "node-b", "CreateIndex": 12, "ModifyIndex": 13, "Job": {"Meta": {"pilot_image": image, "pilot_source_version": "a" * 40, "pilot_hostname": "pilot.example.test"}}}),
            result({"NodePool": "ingress"}),
        ]
        proofs, verified = exercise.inventory_pilot(image, "a" * 40, "pilot.example.test", self.namespace)
        self.assertTrue(verified)
        self.assertEqual(proofs[0]["image"], image)
        self.assertEqual(proofs[0]["sourceVersion"], "a" * 40)
        self.assertEqual({proof["nodeID"] for proof in proofs}, {"node-a", "node-b"})
        self.assertEqual(run.call_args_list[0].args[0], ["nomad", "job", "allocs", "-namespace=" + self.namespace, "-json", "hello-norn-mysql"])
        self.assertEqual(run.call_args_list[1].args[0], ["nomad", "alloc", "status", "-namespace=" + self.namespace, "-json", allocations[0]])

    def test_namespace_requires_exact_fleet_run_id_shape(self):
        for namespace in ("default", "norn-pilot-run1", "norn-pilot-pilot-260907a", "norn-pilot-pilot260907a-"):
            with self.subTest(namespace=namespace):
                self.assertFalse(exercise.PILOT_NAMESPACE.fullmatch(namespace))
        self.assertTrue(exercise.PILOT_NAMESPACE.fullmatch(self.namespace))

    @patch("exercise.time.sleep")
    @patch("exercise.inventory_pilot")
    @patch("exercise.nomad_json")
    def test_fault_recovery_requires_stopped_target_and_replacement(self, nomad_json, inventory, _sleep):
        old = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
        kept = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
        replacement = "cccccccc-cccc-cccc-cccc-cccccccccccc"
        nomad_json.return_value = {"ClientStatus": "complete"}
        inventory.return_value = ([{"allocation": kept}, {"allocation": replacement}], True)
        proofs, replacements, recovered = exercise.wait_for_recovery(old, {old, kept}, "image", "source", "pilot.example.test", self.namespace, 1)
        self.assertTrue(recovered)
        self.assertEqual(proofs[1]["allocation"], replacement)
        self.assertEqual(replacements, [replacement])
        self.assertEqual(
            nomad_json.call_args.args[0],
            ["alloc", "status", "-namespace=" + self.namespace, "-json", old],
        )
        self.assertEqual(inventory.call_args.args[-1], self.namespace)

    @patch("exercise.time.sleep")
    @patch("exercise.request")
    def test_public_recovery_requires_replacement_response(self, request, _sleep):
        replacement = "cccccccc-cccc-cccc-cccc-cccccccccccc"
        request.return_value = {"ok": False, "allocation": ""}
        allocation, recovered = exercise.wait_for_public_replacement(
            "https://pilot.example.test", "receipt", [replacement], self.source, 0.001
        )
        self.assertFalse(recovered)
        self.assertEqual(allocation, "")


if __name__ == "__main__":
    unittest.main()
