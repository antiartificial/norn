import io
import json
import unittest
from unittest.mock import patch

import exercise


class Response(io.BytesIO):
    status = 200


class ProbeTests(unittest.TestCase):
    def probe(self, payload):
        with patch("exercise.urllib.request.build_opener") as opener:
            opener.return_value.open.return_value = Response(payload)
            return exercise.request("https://staging.example.test", "GET", "receipt-1")

    def test_matching_receipt(self):
        self.assertTrue(self.probe(b'{"id":"receipt-1","allocation":"node-a"}')["ok"])

    def test_wrong_receipt_fails(self):
        self.assertFalse(self.probe(b'{"id":"receipt-2"}')["ok"])

    def test_non_object_or_malformed_json_fails(self):
        for payload in (b"[]", b"null", b"not-json"):
            with self.subTest(payload=payload):
                self.assertFalse(self.probe(payload)["ok"])

    def test_oversized_response_fails(self):
        self.assertFalse(self.probe(b" " * 65537)["ok"])

    def test_fault_rejects_short_allocation_prefix(self):
        self.assertFalse(exercise.stop_pilot_allocation("aaaaaaaa"))

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_fault_stop_requires_target_job(self, run, _which):
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({"JobID": "another-job"})
        self.assertFalse(exercise.stop_pilot_allocation("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"))
        self.assertEqual(run.call_count, 1)

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_fault_stop_is_scoped_to_pilot_allocation(self, run, _which):
        inspect = type("Result", (), {"returncode": 0, "stdout": json.dumps({"JobID": "hello-norn-mysql"})})()
        stopped = type("Result", (), {"returncode": 0, "stdout": ""})()
        run.side_effect = [inspect, stopped]
        allocation = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
        self.assertTrue(exercise.stop_pilot_allocation(allocation))
        self.assertEqual(run.call_args_list[1].args[0], ["nomad", "alloc", "stop", "-yes", allocation])

    @patch("exercise.shutil.which", return_value="/usr/bin/nomad")
    @patch("exercise.subprocess.run")
    def test_nomad_evidence_binds_pilot_digest_and_source(self, run, _which):
        allocation = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
        image = "registry.example.test/norn/hello@sha256:" + "a" * 64
        run.return_value = type("Result", (), {"returncode": 0, "stdout": json.dumps({
            "JobID": "hello-norn-mysql", "ClientStatus": "running", "CreateIndex": 10, "ModifyIndex": 11,
            "Job": {"Meta": {"pilot_image": image, "pilot_source_version": "a" * 40}},
        })})()
        proofs, verified = exercise.inspect_pilot_allocations([allocation])
        self.assertTrue(verified)
        self.assertEqual(proofs[0]["image"], image)
        self.assertEqual(proofs[0]["sourceVersion"], "a" * 40)


if __name__ == "__main__":
    unittest.main()
