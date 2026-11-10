"""Individual-unit and quantity-stock wire rules, not runtime handler evidence."""
from copy import deepcopy
import json
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, PROJECTION, SCHEMAS, record, validator


MAXIMUM = 9007199254740991
UNIT = {"id": "unit", "revision": 3, "item_id": "item", "location_id": None,
        "asset_code": "  AST-001 é  ", "serial_number": None, "notes": "", "status": "stored"}
STOCK = {"item_id": "item", "location_id": "location", "revision": 8,
         "quantity_base": 1500, "unit_code": "kg", "base_unit": "g", "scale_factor": 1000}
STOCK_INPUTS = {
    "StockReceiveInput": {"item_id": "item", "target_location_id": "target", "quantity_base": 1},
    "StockConsumeInput": {"item_id": "item", "source_location_id": "source", "quantity_base": 1},
    "StockTransferInput": {"item_id": "item", "source_location_id": "source", "target_location_id": "target", "quantity_base": 1},
    "StockAdjustInput": {"item_id": "item", "target_location_id": "target", "quantity_base": 0, "reason": "  Counted é  "},
}
WRITES = [
    ("/items/{id}/units", "post", "item", "unit", 131072),
    ("/units/{id}", "patch", "unit", "unit", 131072),
    ("/units/{id}", "delete", "unit", None, 0),
    ("/units/{id}/move", "post", "unit", "unit", 16384),
    ("/units/{id}/status", "post", "unit", "unit", 131072),
    *[(f"/stock/{action}", "post", "item", "stock", 131072)
      for action in ["receive", "consume", "transfer", "adjust"]],
]


class HoldingsContractTests(unittest.TestCase):
    def test_exact_query_unit_lookup_preserves_retained_identity_and_independent_etag(self):
        self.assertEqual({"get"}, set(API["paths"]["/units"]))
        operation = API["paths"]["/units"]["get"]
        original = API["paths"]["/units/{id}"]["get"]
        self.assertEqual("get_unit_by_identity", operation["operationId"])
        self.assertEqual(0, operation["x-max-body-bytes"])
        self.assertEqual(16384, operation["x-max-query-bytes"])
        self.assertEqual("unit", operation["x-response-revision-scope"])
        self.assertNotEqual([], operation.get("security", API["security"]))
        self.assertNotIn("requestBody", operation)
        self.assertEqual([("id", "query", True)], [(param["name"], param["in"], param["required"]) for param in operation["parameters"]])
        schema = operation["parameters"][0]["schema"]
        self.assertEqual(SCHEMAS["Unit"]["properties"]["id"], schema)
        identity = Draft202012Validator(schema)
        for valid in ["legacy/item/\x00😀", "tree", ".", "😀" * 128]:
            identity.validate(valid)
        for invalid in ["", "😀" * 129, None, 1]:
            with self.subTest(identity=invalid), self.assertRaises(ValidationError):
                identity.validate(invalid)
        response = operation["responses"]["200"]
        self.assertEqual({"$ref": "#/components/schemas/Unit"}, response["content"]["application/json"]["schema"])
        self.assertEqual({"Cache-Control", "ETag"}, set(response["headers"]))
        self.assertEqual(original["responses"]["200"]["headers"]["ETag"], response["headers"]["ETag"])
        for word in ["exact", "path", "completed session", "SQL", "not a collection"]:
            self.assertIn(word, operation["description"])

    def test_unit_resources_retain_complete_bounded_independent_identity_and_offline_shape(self):
        validator("Unit").validate(UNIT)
        payload = {key: value for key, value in UNIT.items() if key != "revision"}
        Draft202012Validator(PROJECTION).validate(record("unit", payload, identity=UNIT["id"]))
        for key in UNIT:
            with self.subTest(missing=key), self.assertRaises(ValidationError):
                validator("Unit").validate({name: value for name, value in UNIT.items() if name != key})
        maximum = {**UNIT, "id": "😀" * 128, "item_id": "😀" * 128, "location_id": "😀" * 128,
                   "asset_code": "😀" * 10000, "serial_number": "😀" * 10000, "notes": "😀" * 10000,
                   "revision": MAXIMUM}
        validator("Unit").validate(maximum)
        for patch in [{"id": ""}, {"id": "x" * 129}, {"item_id": ""}, {"item_id": "x" * 129},
                      {"location_id": ""}, {"location_id": "x" * 129}, {"asset_code": ""},
                      {"asset_code": "x" * 10001}, {"serial_number": "x" * 10001}, {"notes": None},
                      {"notes": "x" * 10001}, {"revision": 0}, {"revision": -1}, {"revision": 1.5},
                      {"revision": MAXIMUM + 1}, {"status": "archived"}, {"raw_ocr": "private"}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("Unit").validate({**UNIT, **patch})
        for status in ["stored", "in_use", "lent", "lost", "retired", "disposed"]:
            validator("Unit").validate({**UNIT, "status": status})
        validator("Unit").validate({**UNIT, "serial_number": "", "notes": "  Exact é\n  "})

    def test_unit_creation_requires_an_explicit_location_without_caller_owned_identity_or_terminal_status(self):
        for location in [None, "new", "tree", "😀" * 128]:
            for serial in [None, "", "  Serial é  ", "😀" * 10000]:
                validator("UnitCreate").validate({"location_id": location, "serial_number": serial})
        validator("UnitCreate").validate({"location_id": None})
        validator("UnitCreate").validate({"location_id": "location", "notes": "😀" * 10000, "status": "lost"})
        self.assertEqual("stored", SCHEMAS["UnitCreate"]["properties"]["status"]["default"])
        for body in [{}, {"location_id": ""}, {"location_id": "x" * 129}, {"location_id": 1},
                     {"location_id": None, "notes": None}, {"location_id": None, "serial_number": "x" * 10001},
                     {"location_id": None, "notes": "x" * 10001}, {"location_id": None, "status": "retired"},
                     {"location_id": None, "status": "disposed"}]:
            with self.subTest(body=body), self.assertRaises(ValidationError):
                validator("UnitCreate").validate(body)
        for field in ["id", "item_id", "asset_code", "revision", "quantity_base", "purchase", "amount_minor"]:
            with self.subTest(field=field), self.assertRaises(ValidationError):
                validator("UnitCreate").validate({"location_id": None, field: "caller"})

    def test_unit_partial_edits_preserve_null_empty_omitted_text_and_never_change_ownership_or_location(self):
        for body in [{"serial_number": None}, {"serial_number": ""}, {"notes": ""},
                     {"serial_number": "  Exact é  ", "notes": "\x00Manual\ntext"}]:
            validator("UnitWrite").validate(body)
        for body in [{}, {"notes": None}, {"serial_number": 1}, {"notes": "x" * 10001},
                     {"serial_number": "x" * 10001}, {"serial_number": {"set": True, "value": None}},
                     *[{field: "caller"} for field in ["id", "item_id", "asset_code", "location_id", "status", "quantity_base"]]]:
            with self.subTest(body=body), self.assertRaises(ValidationError):
                validator("UnitWrite").validate(body)
        operation = API["paths"]["/units/{id}"]["patch"]
        self.assertEqual({"$ref": "#/components/schemas/UnitWrite"}, operation["requestBody"]["content"]["application/json"]["schema"])

    def test_unit_moves_and_statuses_are_separate_explicit_original_unit_commands(self):
        for location in ["new", "tree", "😀" * 128]:
            validator("UnitMove").validate({"target_location_id": location})
        for body in [{}, {"target_location_id": None}, {"target_location_id": ""},
                     {"target_location_id": "x" * 129}, {"target_location_id": "target", "item_id": "other"}]:
            with self.subTest(body=body), self.assertRaises(ValidationError):
                validator("UnitMove").validate(body)
        for status in ["stored", "in_use", "lent", "lost", "retired"]:
            validator("UnitStatusInput").validate({"status": status, "note": "😀" * 10000})
        for body in [{}, {"status": "archived"}, {"status": None}, {"status": "stored", "note": None},
                     {"status": "stored", "note": "x" * 10001}, {"status": "disposed", "amount_minor": 1}]:
            with self.subTest(body=body), self.assertRaises(ValidationError):
                validator("UnitStatusInput").validate(body)
        self.assertIn("guards", SCHEMAS["UnitStatusInput"]["description"])
        self.assertIn("immutable", SCHEMAS["UnitStatusInput"]["description"])

    def test_non_asset_unit_disposal_requires_an_explicit_kind_and_never_implicit_finances(self):
        for kind in ["sold", "donated", "recycled", "discarded"]:
            validator("UnitStatusInput").validate({"status": "disposed", "disposal_kind": kind, "note": "😀" * 10000})
        for body in [
            {"status": "disposed"}, {"status": "disposed", "disposal_kind": None},
            {"status": "disposed", "disposal_kind": "other"}, {"status": "stored", "disposal_kind": "discarded"},
            {"status": "disposed", "disposal_kind": "sold", "realized_proceeds_minor": 1},
            {"status": "disposed", "disposal_kind": "sold", "currency": "TWD"},
        ]:
            with self.subTest(body=body), self.assertRaises(ValidationError):
                validator("UnitStatusInput").validate(body)
        self.assertIn("Asset-backed", SCHEMAS["UnitStatusInput"]["description"])
        self.assertIn("Retirement preserves physical placement", SCHEMAS["UnitStatusInput"]["description"])

    def test_stock_responses_use_real_pair_identity_safe_base_counts_and_the_complete_item_measurement(self):
        validator("Stock").validate(STOCK)
        payload = {key: value for key, value in STOCK.items() if key != "revision"}
        pair = json.dumps([STOCK["item_id"], STOCK["location_id"]], separators=(",", ":"))
        Draft202012Validator(PROJECTION).validate(record("stock", payload, identity=pair))
        for key in STOCK:
            with self.subTest(missing=key), self.assertRaises(ValidationError):
                validator("Stock").validate({name: value for name, value in STOCK.items() if name != key})
        validator("Stock").validate({**STOCK, "item_id": "😀" * 128, "location_id": "😀" * 128,
                                     "quantity_base": MAXIMUM, "revision": MAXIMUM, "scale_factor": MAXIMUM,
                                     "unit_code": "😀" * 32, "base_unit": "a" * 32})
        for patch in [{"item_id": ""}, {"item_id": "x" * 129}, {"location_id": ""}, {"location_id": None},
                      {"location_id": "x" * 129}, {"revision": 0}, {"revision": MAXIMUM + 1},
                      {"quantity_base": -1}, {"quantity_base": 0.5}, {"quantity_base": MAXIMUM + 1},
                      {"quantity_base": "1500"}, {"quantity_base": True}, {"unit_code": ""},
                      {"unit_code": "x" * 33}, {"base_unit": "g\n"}, {"base_unit": "g "},
                      {"base_unit": "公斤"}, {"base_unit": "x" * 33}, {"scale_factor": 0},
                      {"scale_factor": 1.5}, {"scale_factor": MAXIMUM + 1}, {"quantity_minor": 1},
                      {"quantity_display": 1.5}, {"id": "fictional-row-id"}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("Stock").validate({**STOCK, **patch})
        validator("Stock").validate({**STOCK, "quantity_base": 0})
        for field in ["unit_code", "base_unit", "scale_factor"]:
            self.assertEqual(SCHEMAS["Item"]["properties"][field], SCHEMAS["Stock"]["properties"][field])

    def test_stock_writes_require_exact_safe_quantities_and_only_their_complete_typed_fields(self):
        for name, body in STOCK_INPUTS.items():
            with self.subTest(schema=name):
                validator(name).validate(body)
                validator(name).validate({**body, "quantity_base": MAXIMUM, "note": "😀" * 10000})
                validator(name).validate({**body, "quantity_base": 1.0})
                validator(name).validate({**body, "quantity_base": json.loads("1e3")})
                for key in body:
                    with self.subTest(missing=key), self.assertRaises(ValidationError):
                        validator(name).validate({field: value for field, value in body.items() if field != key})
                for quantity in [-1, MAXIMUM + 1, 0.5, "1", None, True]:
                    with self.subTest(quantity=quantity), self.assertRaises(ValidationError):
                        validator(name).validate({**body, "quantity_base": quantity})
                for field in ["quantity_minor", "quantity_display", "delta", "unit_code", "base_unit", "scale_factor", "revision", "item_unit_id", "amount_minor"]:
                    with self.subTest(field=field), self.assertRaises(ValidationError):
                        validator(name).validate({**body, field: 1})
                for field in [key for key in body if key.endswith("_id")]:
                    validator(name).validate({**body, field: "😀" * 128})
                    for identity in ["", None, "x" * 129]:
                        with self.subTest(field=field, identity=identity), self.assertRaises(ValidationError):
                            validator(name).validate({**body, field: identity})
                for note in [None, "x" * 10001]:
                    with self.subTest(note=note), self.assertRaises(ValidationError):
                        validator(name).validate({**body, "note": note})
                self.assertEqual("stock-item-proof-safe-base-quantity-v1", SCHEMAS[name]["x-runtime-validation"])

    def test_receive_consume_transfer_are_positive_but_adjustment_is_an_absolute_nonnegative_reasoned_set(self):
        for name, body in STOCK_INPUTS.items():
            if name == "StockAdjustInput":
                validator(name).validate({**body, "quantity_base": 0, "reason": "😀" * 10000})
                for reason in [None, "", "x" * 10001]:
                    with self.subTest(reason=reason), self.assertRaises(ValidationError):
                        validator(name).validate({**body, "reason": reason})
            else:
                with self.subTest(schema=name), self.assertRaises(ValidationError):
                    validator(name).validate({**body, "quantity_base": 0})
        # Shape validation cannot certify source/target inequality, available
        # balance, tracking, overflow arithmetic or any transaction/HTTP behavior.
        validator("StockTransferInput").validate({**STOCK_INPUTS["StockTransferInput"], "source_location_id": "same", "target_location_id": "same"})
        for name in STOCK_INPUTS:
            self.assertIn("original Item revision", SCHEMAS[name]["description"])
        self.assertIn("absolute", SCHEMAS["StockAdjustInput"]["description"])
        self.assertIn("retain", SCHEMAS["StockConsumeInput"]["description"])

    def test_complete_live_pages_require_bounded_rows_and_nullable_continuations_without_list_etags(self):
        for name, row, path in [("UnitList", UNIT, "/items/{id}/units"), ("StockList", STOCK, "/inventory")]:
            validator(name).validate({"data": [], "next_cursor": None})
            validator(name).validate({"data": [deepcopy(row) for _ in range(100)], "next_cursor": "opaque"})
            cursor_bound = 4096 if name == "StockList" else 2048
            validator(name).validate({"data": [], "next_cursor": "x" * cursor_bound})
            for body in [{"data": []}, {"data": [], "next_cursor": ""}, {"data": [], "next_cursor": "x" * (cursor_bound + 1)},
                         {"data": [deepcopy(row) for _ in range(101)], "next_cursor": None},
                         {"data": [], "next_cursor": None, "total_quantity_base": 1}]:
                with self.subTest(schema=name, body=body), self.assertRaises(ValidationError):
                    validator(name).validate(body)
            operation = API["paths"][path]["get"]
            params = {entry["name"]: entry for entry in operation["parameters"]}
            self.assertEqual(50, params["limit"]["schema"]["default"])
            self.assertEqual(100, params["limit"]["schema"]["maximum"])
            self.assertEqual(cursor_bound, params["cursor"]["schema"]["maxLength"])
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(16384, operation["x-max-query-bytes"])
            self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        # These two strengthened pages do not silently change other live lists.
        validator("TagList").validate({"data": []})

    def test_inventory_filter_scope_and_composite_cursor_budget_do_not_change_other_collections(self):
        operation = API["paths"]["/inventory"]["get"]
        params = {entry["name"]: entry["schema"] for entry in operation["parameters"]}
        self.assertEqual(
            {"from", "to", "currency", "item_id", "category_id", "location_id", "scope", "status", "merchant", "limit", "cursor"},
            set(params),
        )
        self.assertEqual(["active", "archived"], params["status"]["enum"])
        self.assertEqual("subtree", params["scope"]["default"])
        self.assertIn("same associated purchase", operation["description"])
        self.assertIn("actual balance location", operation["description"])
        self.assertEqual(4096, params["cursor"]["maxLength"])
        for path in ["/items", "/locations/tree", "/categories", "/tags", "/items/{id}/units"]:
            other = {entry["name"]: entry["schema"] for entry in API["paths"][path]["get"]["parameters"]}
            self.assertEqual(2048, other["cursor"]["maxLength"])

    def test_inventory_item_selection_is_bounded_exact_optional_and_not_a_catalog_filter(self):
        operation = API["paths"]["/inventory"]["get"]
        params = {entry["name"]: entry for entry in operation["parameters"]}
        self.assertFalse(params["item_id"]["required"])
        selection = Draft202012Validator(params["item_id"]["schema"])
        for identity in ["new", "tree", "  品項 +?# é  ", "😀" * 128, "\x00" * 128]:
            selection.validate(identity)
        for identity in ["", "😀" * 129, None, 1, [], {}]:
            with self.subTest(identity=identity), self.assertRaises(ValidationError):
                selection.validate(identity)
        self.assertIn("before slicing", operation["description"])
        self.assertIn("never widen item_id", operation["description"])
        self.assertIn("not proof that the Item exists", params["item_id"]["description"])
        for path in ["/items", "/items/{id}/units", "/categories", "/tags", "/locations/tree"]:
            self.assertNotIn("item_id", {entry["name"] for entry in API["paths"][path]["get"]["parameters"]})

    def test_original_item_and_unit_write_proofs_remain_distinct_from_response_row_revisions(self):
        for path, method, scope, response_scope, body_limit in WRITES:
            operation = API["paths"][path][method]
            headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
            with self.subTest(path=path, method=method):
                self.assertEqual(scope, operation["x-revision-scope"])
                self.assertEqual(body_limit, operation["x-max-body-bytes"])
                self.assertEqual(0, operation["x-max-query-bytes"])
                for header in ["Origin", "X-CSRF-Token", "X-HomeBox-Client-Version", "Idempotency-Key", "If-Match"]:
                    self.assertTrue(headers[header]["required"])
                self.assertEqual(2048, headers["Origin"]["schema"]["maxLength"])
                proof = Draft202012Validator(headers["If-Match"]["schema"])
                for valid in ['"1"', f'"{MAXIMUM}"']:
                    proof.validate(valid)
                for invalid in ['1', '"0"', '"01"', '*', 'W/"1"', '"1", "2"', '"1.0"', '"1e3"', '"1"\n', '"12345678901234567"']:
                    with self.subTest(proof=invalid), self.assertRaises(ValidationError):
                        proof.validate(invalid)
                if response_scope:
                    self.assertEqual(response_scope, operation["x-response-revision-scope"])
                    schema = operation["responses"]["200"]["headers"]["ETag"]["schema"]
                    etag = Draft202012Validator(schema)
                    for valid in ['"1"', f'"{MAXIMUM}"']:
                        etag.validate(valid)
                    for invalid in ['1', '"0"', '"01"', 'W/"1"', '"1", "2"', '"1"\n']:
                        with self.assertRaises(ValidationError):
                            etag.validate(invalid)
                else:
                    self.assertNotIn("requestBody", operation)
                    self.assertNotIn("ETag", operation["responses"]["200"]["headers"])

    def test_unit_deletion_acknowledgement_requires_its_independent_next_revision_not_a_list_etag(self):
        operation = API["paths"]["/units/{id}"]["delete"]
        schema = operation["responses"]["200"]["content"]["application/json"]["schema"]
        check = Draft202012Validator({**schema, "components": API["components"]})
        check.validate({"ok": True, "operation_id": "event", "revision": 4})
        for body in [{"ok": True, "operation_id": "event"}, {"ok": True, "operation_id": "event", "revision": 0},
                     {"ok": True, "operation_id": "event", "revision": MAXIMUM + 1}, {"ok": False, "operation_id": "event", "revision": 4}]:
            with self.subTest(body=body), self.assertRaises(ValidationError):
                check.validate(body)
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        # Matching revision == original Unit revision + 1 remains a runtime check.

    def test_stock_successes_return_the_declared_affected_balance_without_reinterpreting_the_item_proof(self):
        for action in ["receive", "consume", "transfer", "adjust"]:
            operation = API["paths"][f"/stock/{action}"]["post"]
            self.assertEqual({"$ref": "#/components/schemas/Stock"}, operation["responses"]["200"]["content"]["application/json"]["schema"])
            self.assertEqual("item", operation["x-revision-scope"])
            self.assertEqual("stock", operation["x-response-revision-scope"])
        # A balance revision need not equal the unrelated reviewed Item revision.
        validator("Stock").validate({**STOCK, "revision": 2})
        self.assertIn("source Stock", SCHEMAS["StockConsumeInput"]["description"])
        self.assertIn("target Stock", SCHEMAS["StockTransferInput"]["description"])

    def test_offline_payload_identities_stay_unchanged_and_do_not_acquire_http_revisions(self):
        for kind, expected in [("unit", {"id", "item_id", "location_id", "asset_code", "status", "serial_number", "notes"}),
                               ("stock", {"item_id", "location_id", "quantity_base", "unit_code", "base_unit", "scale_factor"})]:
            self.assertEqual(expected, set(PROJECTION["$defs"][kind]["properties"]))
            self.assertNotIn("revision", PROJECTION["$defs"][kind]["properties"])
            payload = {key: value for key, value in (UNIT if kind == "unit" else STOCK).items() if key != "revision"}
            identity = UNIT["id"] if kind == "unit" else json.dumps([STOCK["item_id"], STOCK["location_id"]], separators=(",", ":"))
            Draft202012Validator(PROJECTION).validate(record(kind, payload, identity=identity))
            Draft202012Validator(PROJECTION).validate(record(kind, operation="delete", identity=identity))
