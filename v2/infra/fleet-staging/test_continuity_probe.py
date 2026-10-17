import io
import json
import unittest
from unittest.mock import patch

import continuity_probe


class Response(io.BytesIO):
    status = 200


class ContinuityTests(unittest.TestCase):
    def test_matching_existing_record(self):
        record = "a" * 32 + "-pre"
        payload = json.dumps({"id": record, "version": "b" * 40,
                              "allocation": "11111111-1111-1111-1111-111111111111"}).encode()
        with patch("continuity_probe.opener") as open_client:
            open_client.return_value.open.return_value = Response(payload)
            self.assertTrue(continuity_probe.sample("https://staging.test", record, "b" * 40)["ok"])

    def test_rejects_wrong_record_and_oversized_body(self):
        record = "a" * 32 + "-pre"
        with patch("continuity_probe.opener") as open_client:
            open_client.return_value.open.return_value = Response(b'{"id":"other"}')
            self.assertFalse(continuity_probe.sample("https://staging.test", record, "b" * 40)["ok"])
            open_client.return_value.open.return_value = Response(b" " * 65537)
            self.assertFalse(continuity_probe.sample("https://staging.test", record, "b" * 40)["ok"])


if __name__ == "__main__":
    unittest.main()
