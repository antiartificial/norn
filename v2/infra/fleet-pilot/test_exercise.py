import io
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


if __name__ == "__main__":
    unittest.main()
