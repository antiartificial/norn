import copy
import unittest

from compare_state import compare


class CompareStateTest(unittest.TestCase):
    def setUp(self):
        item_id = "a" * 32
        self.state = {
            "schema": "norn.mobility-fixture/v1",
            "writerEnabled": False,
            "items": [{"id": item_id, "sha256": "b" * 64}],
            "pendingJobIds": [],
            "acknowledgedJobIds": [item_id],
            "tickMinutes": ["2026-09-27T08:07:00Z"],
            "jobsPending": 0,
            "jobsAcknowledged": 1,
            "scheduleTicks": 1,
            "filesMissing": 0,
            "filesMismatched": 0,
            "filesOrphaned": 0,
        }

    def test_matching_quiesced_inventories(self):
        self.assertEqual(compare(self.state, copy.deepcopy(self.state)), (1, 1, 1))

    def test_rejects_missing_file_and_live_writer(self):
        for field, value in (("filesMissing", 1), ("writerEnabled", True)):
            target = copy.deepcopy(self.state)
            target[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                compare(self.state, target)

    def test_rejects_data_work_and_schedule_drift(self):
        changes = (
            ("items", [{"id": "a" * 32, "sha256": "c" * 64}]),
            ("acknowledgedJobIds", []),
            ("tickMinutes", []),
        )
        for field, value in changes:
            target = copy.deepcopy(self.state)
            target[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                compare(self.state, target)


if __name__ == "__main__":
    unittest.main()
