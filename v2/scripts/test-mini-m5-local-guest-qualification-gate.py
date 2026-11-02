import importlib.machinery
import importlib.util
from pathlib import Path
import unittest


path = Path(__file__).with_name("mini-m5-local-guest-qualification-gate")
loader = importlib.machinery.SourceFileLoader("m5_qualification_gate", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
gate = importlib.util.module_from_spec(spec)
loader.exec_module(gate)


class QualificationGateTests(unittest.TestCase):
    def test_signed_receipt_must_bind_guest_source_and_isolation(self):
        guest = "norn-m5-12345"
        marker = "a" * 64
        source = "b" * 40
        attestation = {"schema": "norn.m5-isolated-runner-attestation/v1",
                       "runner": "norn-m5-isolated-01", "hostOutbound": "loopback-only",
                       "credentialSurfacesAbsentOrUnreadable": True,
                       "preexistingNetworkSockets": 0, "guiDomain": "gui/501",
                       "sshHostKeySHA256": "d" * 64}
        receipt = {"schema": "norn.m5-local-guest-qualified/v1", "guestName": guest,
                   "createMarkerSHA256": marker, "qualificationRunId": "67890",
                   "sourceSHA": source, "attestationSHA256": "c" * 64,
                   "runner": "norn-m5-isolated-01", "sshHostKeySHA256": "d" * 64,
                   "tartConfigSHA256": "e" * 64}
        self.assertEqual(gate.verify(receipt, attestation, guest, marker, source)["guestName"], guest)
        with self.assertRaisesRegex(ValueError, "does not match"):
            gate.verify(receipt, attestation, "norn-m5-99999", marker, source)
        attestation["hostOutbound"] = "unrestricted"
        with self.assertRaisesRegex(ValueError, "incomplete"):
            gate.verify(receipt, attestation, guest, marker, source)


if __name__ == "__main__":
    unittest.main()
