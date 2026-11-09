import importlib.machinery
import importlib.util
from pathlib import Path
import unittest


path = Path(__file__).with_name("mini-m5-guest-identity-candidate")
loader = importlib.machinery.SourceFileLoader("m5_guest_identity", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
identity = importlib.util.module_from_spec(spec)
loader.exec_module(identity)


class GuestIdentityTests(unittest.TestCase):
    def test_qualification_inherits_signed_guest_and_ssh_identity(self):
        intent = {"schema": "norn.m5-local-guest-hardening-intent/v1",
                  "sourceSHA": "a" * 40, "guestName": "norn-m5-12345",
                  "createMarkerSHA256": "b" * 64, "sshHostKeySHA256": "c" * 64,
                  "tartConfigSHA256": "d" * 64}
        attestation = {"runner": "norn-m5-isolated-01", "sshHostKeySHA256": "c" * 64,
                       "hostOutbound": "loopback-only", "credentialSurfacesAbsentOrUnreadable": True,
                       "preexistingNetworkSockets": 0}
        result = identity.candidate(intent, attestation, "e" * 64, "67890", "a" * 40,
                                    "norn-m5-isolated-01")
        self.assertEqual(result["guestName"], "norn-m5-12345")
        attestation["sshHostKeySHA256"] = "f" * 64
        with self.assertRaisesRegex(ValueError, "differs from signed"):
            identity.candidate(intent, attestation, "e" * 64, "67890", "a" * 40,
                               "norn-m5-isolated-01")


if __name__ == "__main__":
    unittest.main()
