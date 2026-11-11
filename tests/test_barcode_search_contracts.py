"""Exact barcode discovery wire guarantees, separate from runtime authority tests."""
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, validator


class BarcodeSearchContractTests(unittest.TestCase):
    def test_original_operation_has_bounded_exact_query_and_no_list_etag(self):
        operation = API["paths"]["/search/barcodes"]["get"]
        self.assertEqual("get_search_barcodes", operation["operationId"])
        self.assertEqual(0, operation["x-max-body-bytes"])
        self.assertEqual(16384, operation["x-max-query-bytes"])
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        self.assertEqual("no-store", operation["responses"]["200"]["headers"]["Cache-Control"]["schema"]["const"])
        self.assertEqual({"$ref": "#/components/schemas/BarcodeMatches"}, operation["responses"]["200"]["content"]["application/json"]["schema"])
        params = {entry["name"]: entry for entry in operation["parameters"]}
        self.assertEqual({"value", "symbology", "limit", "cursor"}, set(params))
        self.assertTrue(params["value"]["required"])
        self.assertEqual(50, params["limit"]["schema"]["default"])
        for valid in ["0012345678905", "\x00GS1\x1d", "😀" * 512, "invalid-legacy-checksum"]:
            Draft202012Validator(params["value"]["schema"]).validate(valid)
        for field, values in {"value": ["", "😀" * 513, 123], "symbology": ["", "JAN", "ean_13"],
                              "limit": [0, 101, 1.5], "cursor": ["", "x" * 2049]}.items():
            for value in values:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    Draft202012Validator(params[field]["schema"]).validate(value)
        for symbology in ["EAN_13", "EAN_8", "UPC_A", "UPC_E", "CODE_128", "QR_CODE", "OTHER"]:
            Draft202012Validator(params["symbology"]["schema"]).validate(symbology)

    def test_matches_are_independent_bounded_rows_with_a_required_nullable_continuation(self):
        first = {"item_id": "owner-1", "barcode_id": "barcode-1", "symbology": "EAN_13", "normalized_value": "0012345678905"}
        second = {**first, "item_id": "owner-2", "barcode_id": "barcode-2"}
        for page in [{"matches": [], "next_cursor": None}, {"matches": [first, second], "next_cursor": "opaque"},
                     {"matches": [{**first, "item_id": "😀" * 128, "barcode_id": "\x00/legacy", "normalized_value": "legacy invalid checksum"}], "next_cursor": None}]:
            validator("BarcodeMatches").validate(page)
        for invalid in [{"matches": []}, {"matches": [], "next_cursor": ""},
                        {"matches": [], "next_cursor": "x" * 2049}, {"matches": [first] * 101, "next_cursor": None},
                        {"matches": [], "next_cursor": None, "private": "secret"}]:
            with self.subTest(page=invalid), self.assertRaises(ValidationError):
                validator("BarcodeMatches").validate(invalid)
        for patch in [{"item_id": ""}, {"barcode_id": "😀" * 129}, {"symbology": "JAN"},
                      {"normalized_value": ""}, {"normalized_value": "x" * 513}, {"revision": 1}, {"raw_value": "private"}]:
            with self.subTest(row=patch), self.assertRaises(ValidationError):
                validator("BarcodeMatch").validate({**first, **patch})
        for field in first:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("BarcodeMatch").validate({key: value for key, value in first.items() if key != field})


if __name__ == "__main__":
    unittest.main()
