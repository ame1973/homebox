"""Purchase allocation wire rules, not implemented financial service evidence."""
from copy import deepcopy
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, FORMAT, PROJECTION, SCHEMAS, record, validator


MAXIMUM = 9007199254740991
PURCHASE = {"id": "purchase", "revision": 7, "merchant_name": "  Merchant é  ",
            "purchased_at": "2026-11-06T12:00:00Z", "currency": "JPY", "receipt_note": ""}
LINE = {"id": "line", "revision": 1, "purchase_id": "purchase", "item_id": "item",
        "item_unit_id": None, "quantity_base": 1500, "paid_minor": 0, "note": ""}
HEADER = {key: PURCHASE[key] for key in ["merchant_name", "purchased_at", "currency"]}
ALLOCATION = {**{key: LINE[key] for key in ["item_id", "item_unit_id", "quantity_base", "paid_minor"]}, "item_revision": 7}
OPERATIONS = [("/purchases", "get"), ("/purchases", "post"),
              ("/purchases/{id}", "get"), ("/purchases/{id}", "patch"),
              ("/items/{id}/purchases", "get"),
              ("/purchases/{id}/lines", "get"), ("/purchases/{id}/lines", "post")]
PAGES = [("/purchases", "PurchaseList", PURCHASE),
         ("/items/{id}/purchases", "PurchaseList", PURCHASE),
         ("/purchases/{id}/lines", "PurchaseLineList", LINE)]
WRITES = [("/purchases", "post", None, "purchase"),
          ("/purchases/{id}", "patch", "purchase", "purchase"),
          ("/purchases/{id}/lines", "post", "purchase", "purchase_line")]


def parameter(path, method, name):
    return next(entry for entry in API["paths"][path][method]["parameters"] if entry["name"] == name)


class PurchasesContractTests(unittest.TestCase):
    def test_purchase_and_line_resources_require_complete_bounded_independent_identity(self):
        for name, resource in [("Purchase", PURCHASE), ("PurchaseLine", LINE)]:
            validator(name).validate(resource)
            for field in resource:
                with self.subTest(schema=name, missing=field), self.assertRaises(ValidationError):
                    validator(name).validate({key: value for key, value in resource.items() if key != field})
            for revision in [0, -1, 1.5, True, "1", MAXIMUM + 1]:
                with self.subTest(schema=name, revision=revision), self.assertRaises(ValidationError):
                    validator(name).validate({**resource, "revision": revision})
            validator(name).validate({**resource, "revision": MAXIMUM})
            for field in [key for key in resource if key == "id" or key.endswith("_id")]:
                validator(name).validate({**resource, field: "😀" * 128})
                for identity in ["", "😀" * 129, 1, {"value": "id"}]:
                    with self.subTest(schema=name, field=field, identity=identity), self.assertRaises(ValidationError):
                        validator(name).validate({**resource, field: identity})
                if field != "item_unit_id":
                    with self.assertRaises(ValidationError):
                        validator(name).validate({**resource, field: None})
            with self.assertRaises(ValidationError):
                validator(name).validate({**resource, "raw_ocr": "unreviewed"})
        validator("PurchaseLine").validate({**LINE, "item_unit_id": "unit", "quantity_base": 1})

    def test_header_creation_is_complete_but_receipt_text_is_optional_exact_and_bounded(self):
        validator("PurchaseWrite").validate(HEADER)
        validator("PurchaseWrite").validate({**HEADER, "merchant_name": "😀" * 300, "receipt_note": "😀" * 10000})
        validator("PurchaseWrite").validate({**HEADER, "receipt_note": "  Exact é\n  "})
        self.assertEqual("", SCHEMAS["PurchaseWrite"]["properties"]["receipt_note"]["default"])
        for field in HEADER:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("PurchaseWrite").validate({key: value for key, value in HEADER.items() if key != field})
        for patch in [{"merchant_name": ""}, {"merchant_name": "😀" * 301}, {"merchant_name": None},
                      {"receipt_note": None}, {"receipt_note": "😀" * 10001}, {"currency": "jpy"},
                      {"currency": "JP"}, {"currency": "JPY\n"}]:
            for name, body in [("PurchaseWrite", HEADER), ("Purchase", PURCHASE)]:
                with self.subTest(schema=name, patch_fields=tuple(patch)), self.assertRaises(ValidationError):
                    validator(name).validate({**body, **patch})
        for field in ["id", "revision", "paid_minor", "total_minor", "quantity_base", "lines", "location_id", "asset"]:
            with self.subTest(field=field), self.assertRaises(ValidationError):
                validator("PurchaseWrite").validate({**HEADER, field: "caller"})

    def test_header_patch_is_nonempty_partial_and_does_not_rewrite_allocations(self):
        for body in [{"merchant_name": "  Exact é  "}, {"receipt_note": ""},
                     {"receipt_note": "😀" * 10000}, {"currency": "TWD"},
                     {"purchased_at": "2026-11-06T00:00:00+08:00"}]:
            validator("PurchasePatch").validate(body)
        for body in [{}, {"merchant_name": ""}, {"merchant_name": "😀" * 301},
                     {"receipt_note": None}, {"receipt_note": "😀" * 10001}, {"currency": "JPY\n"},
                     {"paid_minor": 1}, {"lines": []}, {"purchase_id": "other"}, {"revision": 7}]:
            with self.subTest(body_fields=tuple(body)), self.assertRaises(ValidationError):
                validator("PurchasePatch").validate(body)
        description = SCHEMAS["PurchasePatch"]["description"]
        self.assertIn("currency cannot change after allocations exist", description)
        self.assertIn("immutable audit", description)
        self.assertIn("no-op", description)

    def test_allocation_requires_explicit_nullable_unit_intent_and_no_implicit_receipt(self):
        validator("PurchaseLineWrite").validate(ALLOCATION)
        validator("PurchaseLineWrite").validate({**ALLOCATION, "note": "😀" * 10000})
        validator("PurchaseLineWrite").validate({**ALLOCATION, "item_id": "😀" * 128, "note": "  é\n  "})
        for field in ALLOCATION:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("PurchaseLineWrite").validate({key: value for key, value in ALLOCATION.items() if key != field})
        for patch in [{"item_id": ""}, {"item_id": "😀" * 129}, {"item_unit_id": ""},
                      {"item_unit_id": "😀" * 129}, {"item_unit_id": False}, {"note": None}, {"note": "😀" * 10001}]:
            with self.subTest(patch_fields=tuple(patch)), self.assertRaises(ValidationError):
                validator("PurchaseLineWrite").validate({**ALLOCATION, **patch})
        for field in ["id", "purchase_id", "revision", "currency", "quantity_minor", "quantity_display",
                      "unit_price", "unit_code", "base_unit", "scale_factor", "location_id", "asset"]:
            with self.subTest(field=field), self.assertRaises(ValidationError):
                validator("PurchaseLineWrite").validate({**ALLOCATION, field: "caller"})
        description = SCHEMAS["PurchaseLineWrite"]["description"]
        for phrase in ["original Purchase revision", "does not receive stock", "same Item", "asset", "immutable"]:
            self.assertIn(phrase, description)
        self.assertEqual("purchase-allocation-item-proof-safe-minor-base-v2", SCHEMAS["PurchaseLineWrite"]["x-runtime-validation"])

    def test_first_allocation_binds_the_reviewed_item_interpretation_separately_from_purchase_proof(self):
        for revision in [1, MAXIMUM]:
            validator("PurchaseLineWrite").validate({**ALLOCATION, "item_revision": revision})
        for revision in [0, -1, True, "1", None, 1.5, MAXIMUM + 1]:
            with self.subTest(revision=revision), self.assertRaises(ValidationError):
                validator("PurchaseLineWrite").validate({**ALLOCATION, "item_revision": revision})
        self.assertIn("original Item revision", SCHEMAS["PurchaseLineWrite"]["description"])
        self.assertNotIn("item_revision", SCHEMAS["PurchaseLine"]["properties"])
        self.assertNotIn("item_revision", PROJECTION["$defs"]["purchase_line"]["properties"])

    def test_one_explicit_unit_allocation_is_one_unit_not_a_bulk_price_multiplier(self):
        for unit_id in ["new", "tree", "😀" * 128]:
            validator("PurchaseLineWrite").validate({**ALLOCATION, "item_unit_id": unit_id, "quantity_base": 1})
            for quantity in [0, 2, 1500, MAXIMUM]:
                with self.subTest(unit=unit_id, quantity=quantity), self.assertRaises(ValidationError):
                    validator("PurchaseLineWrite").validate({**ALLOCATION, "item_unit_id": unit_id, "quantity_base": quantity})
        validator("PurchaseLineWrite").validate({**ALLOCATION, "quantity_base": MAXIMUM})
        # Ownership, tracking, currency and financial-basis compatibility require
        # actual source validation; JSON shape alone is not SQL or service evidence.

    def test_amount_and_base_count_are_separate_exact_safe_integer_domains(self):
        for name, body in [("PurchaseLine", LINE), ("PurchaseLineWrite", ALLOCATION)]:
            validator(name).validate({**body, "paid_minor": 0})
            validator(name).validate({**body, "quantity_base": MAXIMUM, "paid_minor": MAXIMUM})
            validator(name).validate({**body, "quantity_base": 1.0, "paid_minor": 1e3})
            for field in ["quantity_base", "paid_minor"]:
                for value in [-1, 0.5, True, "1000", None, MAXIMUM + 1]:
                    with self.subTest(schema=name, field=field, value=value), self.assertRaises(ValidationError):
                        validator(name).validate({**body, field: value})
            with self.assertRaises(ValidationError):
                validator(name).validate({**body, "quantity_base": 0})
        # paid_minor is the complete allocated amount, never a per-unit amount;
        # a zero-priced line still creates real retained financial history.

    def test_purchase_instants_are_bounded_strict_rfc3339_not_guessed_civil_dates(self):
        for name, body in [("Purchase", PURCHASE), ("PurchaseWrite", HEADER), ("PurchasePatch", {})]:
            for instant in ["0001-01-01T00:00:00Z", "2026-11-06T00:00:00+08:00",
                            "2026-11-06T00:00:00.123456789-12:00", "9999-12-31T23:59:59Z"]:
                validator(name).validate({**body, "purchased_at": instant})
            for instant in ["", "2026-11-06", "2026-11-06t00:00:00z", "2026-11-06 00:00:00Z",
                            "2026-11-06T0:00:00Z", "2026-11-06T00:00:00,1Z", "2026-11-06T00:00:60Z",
                            "2026-11-06T00:00:00+24:00", "2026-11-06T00:00:00+00:60",
                            "2026-11-06T00:00:00.1234567890Z", "2026-02-30T00:00:00Z",
                            "0000-01-01T00:00:00Z", "10000-01-01T00:00:00Z", "2026-11-06T00:00:00Z\n", None]:
                with self.subTest(schema=name, instant=instant), self.assertRaises(ValidationError):
                    validator(name).validate({**body, "purchased_at": instant})
        # Asset finance now explicitly shares the same lossless instant policy;
        # the underlying legacy aliases used by unrelated schemas stay unchanged.
        self.assertEqual(SCHEMAS["Purchase"]["properties"]["purchased_at"], SCHEMAS["Usage"]["properties"]["occurred_at"])
        self.assertEqual({"type": "string", "format": "date-time"}, SCHEMAS["AnalyticsGroups"]["properties"]["as_of"])

    def test_live_pages_require_complete_bounded_data_and_explicit_terminal_cursors(self):
        for path, name, resource in PAGES:
            validator(name).validate({"data": [], "next_cursor": None})
            validator(name).validate({"data": [deepcopy(resource) for _ in range(100)], "next_cursor": "x" * 2048})
            for body in [{"data": []}, {"data": [], "next_cursor": ""},
                         {"data": [], "next_cursor": "x" * 2049},
                         {"data": [deepcopy(resource) for _ in range(101)], "next_cursor": None},
                         {"data": [], "next_cursor": None, "total_minor": 0}]:
                with self.subTest(path=path, fields=tuple(body)), self.assertRaises(ValidationError):
                    validator(name).validate(body)
            operation = API["paths"][path]["get"]
            self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(16384, operation["x-max-query-bytes"])
            self.assertEqual({"type": "integer", "minimum": 1, "maximum": 100, "default": 50}, parameter(path, "get", "limit")["schema"])
            self.assertEqual({"type": "string", "minLength": 1, "maxLength": 2048}, parameter(path, "get", "cursor")["schema"])
        validator("TagList").validate({"data": []})

    def test_write_admission_and_original_purchase_proofs_are_explicit(self):
        for path, method, scope, response_scope in WRITES:
            operation = API["paths"][path][method]
            headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
            for name in ["Origin", "X-CSRF-Token", "Idempotency-Key", "X-HomeBox-Client-Version"]:
                self.assertTrue(headers[name]["required"])
            self.assertEqual(2048, headers["Origin"]["schema"]["maxLength"])
            self.assertEqual(131072, operation["x-max-body-bytes"])
            self.assertEqual(0, operation["x-max-query-bytes"])
            self.assertEqual(response_scope, operation["x-response-revision-scope"])
            self.assertEqual("application/json", operation["x-content-type"])
            if scope is None:
                self.assertNotIn("If-Match", headers)
            else:
                self.assertEqual(scope, operation["x-revision-scope"])
                self.assertTrue(headers["If-Match"]["required"])
                self.check_strong_proof(headers["If-Match"]["schema"])
        self.assertIn("independent", API["paths"]["/purchases/{id}/lines"]["post"]["description"])
        self.assertIn("not the Item or Unit revision", parameter("/purchases/{id}/lines", "post", "If-Match")["description"])

    def check_strong_proof(self, schema):
        check = Draft202012Validator(schema)
        for value in ['"1"', f'"{MAXIMUM}"']:
            check.validate(value)
        for value in ['1', '"0"', '"01"', 'W/"1"', '*', '"1", "2"', '"1.0"', '"1e3"', '"1"\n', '"12345678901234567"']:
            with self.subTest(proof=value), self.assertRaises(ValidationError):
                check.validate(value)

    def test_only_single_resources_return_their_own_strong_etag(self):
        for path, method in [("/purchases", "post"), ("/purchases/{id}", "get"),
                             ("/purchases/{id}", "patch"), ("/purchases/{id}/lines", "post")]:
            operation = API["paths"][path][method]
            self.check_strong_proof(operation["responses"]["200"]["headers"]["ETag"]["schema"])
        detail = API["paths"]["/purchases/{id}"]["get"]
        self.assertNotIn("requestBody", detail)
        self.assertEqual(0, detail["x-max-body-bytes"])
        self.assertEqual(0, detail["x-max-query-bytes"])
        # A newly appended Line starts at one while its Purchase aggregate can
        # have any valid revision; matching these independent counters is wrong.
        validator("PurchaseLine").validate({**LINE, "revision": 1})

    def test_global_filters_have_exact_complete_scope_and_civil_date_semantics(self):
        operation = API["paths"]["/purchases"]["get"]
        params = {entry["name"]: entry["schema"] for entry in operation["parameters"]}
        self.assertEqual({"from", "to", "currency", "category_id", "location_id", "scope", "status", "merchant", "limit", "cursor"}, set(params))
        self.assertEqual(["active", "archived"], params["status"]["enum"])
        self.assertEqual("subtree", params["scope"]["default"])
        for field in ["from", "to"]:
            check = Draft202012Validator(params[field], format_checker=FORMAT)
            for value in ["0001-01-01", "2026-11-06", "9999-12-31"]:
                check.validate(value)
            for value in ["0000-01-01", "2026-2-01", "2026-02-30", "2026-11-06T00:00:00Z", "2026-11-06\n"]:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    check.validate(value)
        for phrase in ["same line", "home timezone", "before slicing", "exact stored merchant", "15 minutes", "not frozen snapshots"]:
            self.assertIn(phrase, operation["description"])
        for path in ["/items/{id}/purchases", "/purchases/{id}/lines"]:
            self.assertEqual({"id", "limit", "cursor"}, {entry["name"] for entry in API["paths"][path]["get"]["parameters"]})
            self.assertIn("actual", API["paths"][path]["get"]["description"])

    def test_existing_operation_identities_and_read_only_body_rules_are_preserved(self):
        self.assertEqual(7, len(OPERATIONS))
        for path, method in OPERATIONS:
            operation = API["paths"][path][method]
            self.assertEqual(method + "_" + path.strip("/").replace("{", "").replace("}", "").replace("/", "_"), operation["operationId"])
            self.assertNotEqual([], operation.get("security", API["security"]))
            if method == "get":
                self.assertNotIn("requestBody", operation)
            for entry in operation["parameters"]:
                if entry["in"] == "path":
                    self.assertEqual({"type": "string", "minLength": 1, "maxLength": 128}, entry["schema"])
        self.assertNotIn("delete", API["paths"]["/purchases/{id}"])
        self.assertNotIn("/purchase-lines/{id}", API["paths"])

    def test_offline_purchase_payloads_keep_their_original_separate_revision_envelope(self):
        for kind, resource in [("purchase", PURCHASE), ("purchase_line", LINE)]:
            payload = {key: value for key, value in resource.items() if key != "revision"}
            Draft202012Validator(PROJECTION).validate(record(kind, payload, identity=resource["id"]))
            Draft202012Validator(PROJECTION).validate(record(kind, operation="delete", identity=resource["id"]))
            self.assertEqual(set(payload), set(PROJECTION["$defs"][kind]["properties"]))
            self.assertNotIn("revision", PROJECTION["$defs"][kind]["properties"])
            with self.assertRaises(ValidationError):
                Draft202012Validator(PROJECTION).validate(record(kind, {**payload, "revision": 1}, identity=resource["id"]))
        self.assertEqual({"merchant_name", "purchased_at", "currency", "receipt_note", "id"}, set(PROJECTION["$defs"]["purchase"]["properties"]))
