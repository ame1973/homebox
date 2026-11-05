"""Owned catalog wire guarantees, separate from not-yet-implemented handlers."""
from copy import deepcopy
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, PROJECTION, SCHEMAS, record, validator


COLLECTIONS = {"barcodes": "BarcodeList", "aliases": "AliasList",
               "attributes": "AttributeList", "tags": "ItemTagList"}
ROWS = {
    "Barcode": {"id": "barcode", "item_id": "item", "symbology": "EAN_13",
                "raw_value": "0123456789012", "normalized_value": "0123456789012", "revision": 1},
    "Alias": {"id": "alias", "item_id": "item", "alias": "  Exact é  ", "revision": 1},
    "Attribute": {"id": "attribute", "item_id": "item", "attribute_key": "Specification",
                  "attribute_value": "", "revision": 1},
}


class CatalogLinkContractTests(unittest.TestCase):
    def test_rows_have_independent_bounded_identity_revision_and_complete_offline_payloads(self):
        for name, row in ROWS.items():
            with self.subTest(schema=name):
                validator(name).validate(row)
                payload = {key: value for key, value in row.items() if key != "revision"}
                Draft202012Validator(PROJECTION).validate(record(name.lower(), payload, identity=row["id"]))
                for field in row:
                    with self.subTest(missing=field), self.assertRaises(ValidationError):
                        validator(name).validate({key: value for key, value in row.items() if key != field})
                for patch in [{"id": ""}, {"id": "😀" * 129}, {"item_id": ""}, {"item_id": "😀" * 129},
                              {"revision": 0}, {"revision": -1}, {"revision": 0.5},
                              {"revision": 9007199254740992}, {"raw_ocr": "private"}]:
                    with self.subTest(patch=patch), self.assertRaises(ValidationError):
                        validator(name).validate({**row, **patch})
                maximum = {**row, "id": "😀" * 128, "item_id": "😀" * 128, "revision": 9007199254740991}
                validator(name).validate(maximum)

    def test_owned_text_has_exact_independent_limits_and_no_writable_identity(self):
        cases = [("Alias", "AliasWrite", {"alias": "😀" * 300}, {"alias": "x" * 301}),
                 ("Attribute", "AttributeWrite", {"attribute_key": "😀" * 100, "attribute_value": "😀" * 10000},
                  {"attribute_key": "x" * 101, "attribute_value": ""}),
                 ("Barcode", "BarcodeWrite", {"symbology": "QR_CODE", "raw_value": "😀" * 512},
                  {"symbology": "QR_CODE", "raw_value": "x" * 513})]
        for resource, write, valid, invalid in cases:
            validator(write).validate(valid)
            validator(resource).validate({**ROWS[resource], **valid, **({"normalized_value": valid["raw_value"]} if resource == "Barcode" else {})})
            for patch in [{"id": "caller-id"}, {"item_id": "other-owner"}, {"revision": 1},
                          {"normalized_value": "caller-normalization"}, {"quantity_base": 1}]:
                with self.subTest(write=write, patch=patch), self.assertRaises(ValidationError):
                    validator(write).validate({**valid, **patch})
            with self.subTest(write=write), self.assertRaises(ValidationError):
                validator(write).validate(invalid)
        for alias in ["", None, {"set": True, "value": "Alias"}]:
            with self.assertRaises(ValidationError):
                validator("AliasWrite").validate({"alias": alias})
        with self.assertRaises(ValidationError):
            validator("AttributeWrite").validate({"attribute_key": "Key", "attribute_value": "x" * 10001})

    def test_attribute_creation_and_partial_changes_do_not_overwrite_omitted_fields(self):
        for patch in [{"attribute_key": "New key"}, {"attribute_value": ""},
                      {"attribute_key": "New key", "attribute_value": "  Exact é  "}]:
            validator("AttributePatch").validate(patch)
        for invalid in [{}, {"attribute_key": ""}, {"attribute_value": None},
                        {"attribute_value": {"set": True, "value": None}}, {"item_id": "other"}]:
            with self.subTest(invalid=invalid), self.assertRaises(ValidationError):
                validator("AttributePatch").validate(invalid)
        for incomplete in [{"attribute_key": "Key"}, {"attribute_value": ""}]:
            with self.assertRaises(ValidationError):
                validator("AttributeWrite").validate(incomplete)
        operation = API["paths"]["/attributes/{id}"]["patch"]
        self.assertEqual({"$ref": "#/components/schemas/AttributePatch"},
                         operation["requestBody"]["content"]["application/json"]["schema"])

    def test_barcode_width_and_ascii_rules_preserve_strings_and_require_runtime_checksum_validation(self):
        valid = {"EAN_13": "4006381333931", "EAN_8": "96385074", "UPC_A": "036000291452",
                 "UPC_E": "01234565", "CODE_128": "  Leading 001 é".replace("é", "ASCII") + "\x1d",
                 "QR_CODE": "https://example.invalid/外部文字", "OTHER": "  原始字元 é  "}
        for symbology, raw in valid.items():
            validator("BarcodeWrite").validate({"symbology": symbology, "raw_value": raw})
            self.assertIsInstance(raw, str)
        for symbology, raw in [("EAN_13", "400638133393"), ("EAN_13", "4006381333931\n"),
                               ("EAN_13", "４006381333931"), ("EAN_8", "9638 074"),
                               ("UPC_A", " 036000291452"), ("UPC_A", "036000291452-"),
                               ("UPC_E", "21234565"), ("UPC_E", "123456"),
                               ("CODE_128", "非 ASCII"), ("EAN_13", 4006381333931),
                               ("OTHER", ""), ("JAN", "4006381333931")]:
            with self.subTest(symbology=symbology, raw=raw), self.assertRaises(ValidationError):
                validator("BarcodeWrite").validate({"symbology": symbology, "raw_value": raw})
        # JSON Schema checks shape, not GTIN arithmetic or authoritative normalization.
        validator("BarcodeWrite").validate({"symbology": "EAN_13", "raw_value": "4006381333932"})
        self.assertEqual("gtin-check-digit-and-exact-normalization-v1", SCHEMAS["BarcodeWrite"]["x-runtime-validation"])
        validator("BarcodeWrite").validate({"symbology": "CODE_128", "raw_value": "\x00"})

    def test_owned_lists_require_complete_bounded_live_pages_without_claiming_a_snapshot(self):
        for kind, name in COLLECTIONS.items():
            validator(name).validate({"data": [], "next_cursor": None})
            row = {"id": "tag", "revision": 1, "name": "Tag"} if kind == "tags" else ROWS[{"barcodes": "Barcode", "aliases": "Alias", "attributes": "Attribute"}[kind]]
            validator(name).validate({"data": [row], "next_cursor": "opaque"})
            for invalid in [{"data": []}, {"data": [], "next_cursor": ""},
                            {"data": [], "next_cursor": "x" * 2049},
                            {"data": [deepcopy(row) for _ in range(101)], "next_cursor": None},
                            {"data": [], "next_cursor": None, "raw_ocr": "private"}]:
                with self.subTest(schema=name, invalid=invalid), self.assertRaises(ValidationError):
                    validator(name).validate(invalid)
            operation = API["paths"][f"/items/{{id}}/{kind}"]["get"]
            params = {entry["name"]: entry for entry in operation["parameters"]}
            self.assertEqual(50, params["limit"]["schema"]["default"])
            self.assertEqual(100, params["limit"]["schema"]["maximum"])
            self.assertEqual(2048, params["cursor"]["schema"]["maxLength"])
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(16384, operation["x-max-query-bytes"])
            self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        # The implemented global taxonomy list contract is not silently strengthened.
        validator("TagList").validate({"data": []})

    def test_creation_uses_owner_proof_but_row_edits_and_deletion_use_independent_row_proofs(self):
        for kind in ["barcodes", "aliases", "attributes"]:
            for method, path, scope in [("post", f"/items/{{id}}/{kind}", "item"),
                                         ("patch", f"/{kind}/{{id}}", "row"),
                                         ("delete", f"/{kind}/{{id}}", "row")]:
                operation = API["paths"][path][method]
                headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
                with self.subTest(path=path, method=method):
                    self.assertEqual(scope, operation["x-revision-scope"])
                    self.assertTrue(headers["Origin"]["required"])
                    self.assertTrue(headers["If-Match"]["required"])
                    self.assertEqual(2048, headers["Origin"]["schema"]["maxLength"])
                    proof = Draft202012Validator(headers["If-Match"]["schema"])
                    for valid in ['"1"', '"9007199254740991"']:
                        proof.validate(valid)
                    for invalid in ['1', '"0"', '"01"', '*', 'W/"1"', '"1", "2"']:
                        with self.assertRaises(ValidationError):
                            proof.validate(invalid)
                    limit = 0 if method == "delete" else 131072 if kind == "attributes" else 16384
                    self.assertEqual(limit, operation["x-max-body-bytes"])
                    if method == "delete":
                        self.assertNotIn("requestBody", operation)
                        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
                    else:
                        etag = Draft202012Validator(operation["responses"]["200"]["headers"]["ETag"]["schema"])
                        etag.validate('"1"')
                        etag.validate('"9007199254740991"')
                        for invalid in ['1', '"0"', '"01"', '*', 'W/"1"', '"1", "2"', '"12345678901234567"']:
                            with self.assertRaises(ValidationError):
                                etag.validate(invalid)

    def test_shared_product_values_and_repeated_attribute_keys_do_not_replace_independent_ids(self):
        for owner in ["owner-a", "owner-b"]:
            value = {**ROWS["Barcode"], "id": f"barcode-{owner}", "item_id": owner}
            validator("Barcode").validate(value)
            Draft202012Validator(PROJECTION).validate(record("barcode", {key: entry for key, entry in value.items() if key != "revision"}, identity=value["id"]))
        for identity, value in [("first", "A"), ("second", "B")]:
            validator("Attribute").validate({**ROWS["Attribute"], "id": identity, "attribute_key": "Repeated", "attribute_value": value})
        for incomplete in [{"symbology": "EAN_13"}, {"raw_value": "4006381333931"}]:
            with self.assertRaises(ValidationError):
                validator("BarcodeWrite").validate(incomplete)

    def test_tag_replacement_is_atomic_owner_scoped_and_returns_the_item_not_a_partial_tag_page(self):
        for ids in [[], ["tag"], ["😀" * 128], [f"tag-{index}" for index in range(100)]]:
            validator("ItemTagsInput").validate({"tag_ids": ids})
        for ids in [[""], ["😀" * 129], [None], ["same", "same"], [f"tag-{index}" for index in range(101)]]:
            with self.subTest(ids=ids), self.assertRaises(ValidationError):
                validator("ItemTagsInput").validate({"tag_ids": ids})
        for extra in [{"item_id": "other"}, {"tag_names": ["Unreviewed"]}, {"quantity_base": 1}]:
            with self.assertRaises(ValidationError):
                validator("ItemTagsInput").validate({"tag_ids": [], **extra})
        get = API["paths"]["/items/{id}/tags"]["get"]
        put = API["paths"]["/items/{id}/tags"]["put"]
        self.assertEqual({"$ref": "#/components/schemas/ItemTagList"}, get["responses"]["200"]["content"]["application/json"]["schema"])
        self.assertEqual({"$ref": "#/components/schemas/Item"}, put["responses"]["200"]["content"]["application/json"]["schema"])
        self.assertEqual("item", put["x-revision-scope"])
        self.assertEqual(131072, put["x-max-body-bytes"])
        self.assertTrue(next(entry for entry in put["parameters"] if entry["name"] == "Origin")["required"])
        self.assertIn("Strong quoted owning item", put["responses"]["200"]["headers"]["ETag"]["description"])


if __name__ == "__main__":
    unittest.main()
