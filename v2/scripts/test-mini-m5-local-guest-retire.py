import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-retire")
loader = importlib.machinery.SourceFileLoader("m5_retire", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
retire = importlib.util.module_from_spec(spec)
loader.exec_module(retire)


class RetireTests(unittest.TestCase):
    def test_retire_exact_guest_and_prove_absence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "runId": "67890", "createMarkerSHA256": "a" * 64}
            with patch.object(retire, "verify"), patch.object(retire, "inventory", side_effect=[
                    [{"Name": "other"}, {"Name": "norn-m5-12345", "Running": True}],
                    [{"Name": "other"}]]), patch.object(retire.subprocess, "run") as run:
                receipt = retire.retire(intent, root, "/pinned/tart")
            self.assertEqual(json.loads(receipt.read_text())["after"], [])
            self.assertEqual(run.call_count, 2)
            self.assertEqual(run.call_args_list[0].args[0], ("/pinned/tart", "stop", "norn-m5-12345"))
            self.assertEqual(run.call_args_list[1].args[0], ("/pinned/tart", "delete", "norn-m5-12345"))
            with patch.object(retire, "verify"), patch.object(retire, "inventory", return_value=[]):
                with self.assertRaises(FileExistsError):
                    retire.retire(intent, root, "/pinned/tart")

    def test_duplicate_identity_has_no_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345"}
            with patch.object(retire, "verify"), patch.object(retire, "inventory", return_value=[
                    {"Name": "norn-m5-12345"}, {"Name": "norn-m5-12345"}]), \
                    patch.object(retire.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "duplicate"):
                    retire.retire(intent, root, "/pinned/tart")
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
