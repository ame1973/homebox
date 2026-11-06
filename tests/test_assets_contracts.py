"""Asset financial boundaries; schema checks are not service or device evidence."""
from copy import deepcopy
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, FORMAT, PROJECTION, SCHEMAS, record, validator


MAXIMUM = 9007199254740991
INSTANT = "2026-11-07T12:00:00.123456789+08:00"
ASSET = {"item_unit_id": "unit", "revision": 7, "cost_basis": "manual", "currency": "JPY",
         "manual_acquisition_minor": 0, "in_service_at": None, "retired_at": None,
         "estimated_residual_minor": None, "usage_unit": "times"}
PROFILE = {**{key: value for key, value in ASSET.items() if key not in {"item_unit_id", "revision"}},
           "item_revision": 11, "asset_revision": None}
USAGE = {"id": "use", "revision": 1, "item_unit_id": "unit", "delta": 1, "usage_unit": "times",
         "event_type": "use", "occurred_at": INSTANT, "note": "", "correction_of": None}
USE = {key: value for key, value in USAGE.items() if key not in {"id", "revision", "item_unit_id", "note", "correction_of"}}
COST = {"id": "cost", "revision": 1, "item_unit_id": "unit", "amount_minor": 5000,
        "currency": "JPY", "kind": "repair", "direction": "expense", "occurred_at": INSTANT,
        "note": "", "correction_of": None}
EXPENSE = {key: value for key, value in COST.items() if key not in {"id", "revision", "item_unit_id", "note", "correction_of"}}
DISPOSAL = {"item_unit_id": "unit", "revision": 1, "kind": "sold", "disposed_at": INSTANT,
            "realized_proceeds_minor": 20000, "currency": "JPY", "note": ""}
DISPOSE = {**{key: value for key, value in DISPOSAL.items() if key not in {"item_unit_id", "revision", "note"}},
           "unit_revision": 13}
RETIRE = {"retired_at": INSTANT, "unit_revision": 13}
SUMMARY = {"unit_id": "unit", "currency": "JPY", "cost_basis": "manual", "acquisition_minor": 120000,
           "extra_minor": 5000, "realized_proceeds_minor": 20000, "estimated_residual_minor": 20000,
           "net_cost_minor": 105000, "projected_net_cost_minor": 105000, "elapsed_days": 730,
           "total_usage": 250, "usage_unit": "times", "per_day_minor": 105000 / 730,
           "per_use_minor": 420, "projected_per_day_minor": 105000 / 730, "projected_per_use_minor": 420,
           "as_of": INSTANT, "timezone": "Asia/Tokyo", "day_policy": "inclusive_civil_days", "source_revision": 0}
RESOURCES = [("Asset", ASSET, "asset"), ("Usage", USAGE, "usage"),
             ("Cost", COST, "cost"), ("Disposal", DISPOSAL, "disposal")]
WRITES = [("/units/{id}/asset", "put", "unit", "asset"),
          ("/units/{id}/usage", "post", "asset", "usage"),
          ("/units/{id}/costs", "post", "asset", "cost"),
          ("/units/{id}/retire", "post", "asset", "unit"),
          ("/units/{id}/disposal", "post", "asset", "disposal")]
PAGES = [("/assets", "AssetList", ASSET), ("/units/{id}/usage", "UsageList", USAGE),
         ("/units/{id}/costs", "CostList", COST)]
READS = [path for path, _, _ in PAGES] + ["/units/{id}/asset", "/units/{id}/disposal", "/units/{id}/cost-summary"]


def parameter(path, method, name):
    return next(value for value in API["paths"][path][method]["parameters"] if value["name"] == name)


class AssetsContractTests(unittest.TestCase):
    def test_resources_are_complete_bounded_and_independently_versioned(self):
        for name, resource, _ in RESOURCES:
            validator(name).validate(resource)
            self.assertEqual(set(resource), set(SCHEMAS[name]["required"]))
            for field in resource:
                with self.subTest(schema=name, missing=field), self.assertRaises(ValidationError):
                    validator(name).validate({key: value for key, value in resource.items() if key != field})
            for field in [key for key in resource if key in {"id", "item_unit_id"}]:
                validator(name).validate({**resource, field: "😀" * 128})
                for value in ["", "😀" * 129, None, 1, {"value": "unit"}]:
                    with self.subTest(schema=name, field=field, value=value), self.assertRaises(ValidationError):
                        validator(name).validate({**resource, field: value})
            validator(name).validate({**resource, "revision": MAXIMUM})
            for value in [0, -1, MAXIMUM + 1, 1.5, True, "1"]:
                with self.subTest(schema=name, revision=value), self.assertRaises(ValidationError):
                    validator(name).validate({**resource, "revision": value})
            with self.assertRaises(ValidationError):
                validator(name).validate({**resource, "raw_ocr": "unreviewed"})

    def test_profile_replacement_requires_explicit_original_item_and_nullable_asset_intent(self):
        validator("AssetWrite").validate(PROFILE)
        validator("AssetWrite").validate({**PROFILE, "asset_revision": 7})
        validator("AssetWrite").validate({**PROFILE, "cost_basis": "purchase", "manual_acquisition_minor": None})
        for field in PROFILE:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("AssetWrite").validate({key: value for key, value in PROFILE.items() if key != field})
        for field in ["item_revision", "asset_revision"]:
            for value in [0, -1, MAXIMUM + 1, True, "7", 1.5]:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    validator("AssetWrite").validate({**PROFILE, field: value})
        with self.assertRaises(ValidationError):
            validator("AssetWrite").validate({**PROFILE, "item_revision": None})
        for extra in ["unit_revision", "item_unit_id", "revision", "total_minor", "quantity_base"]:
            with self.subTest(extra=extra), self.assertRaises(ValidationError):
                validator("AssetWrite").validate({**PROFILE, extra: 7})
        description = SCHEMAS["AssetWrite"]["description"]
        for phrase in ["original Unit", "original Item", "explicit null", "existing Asset", "zero", "immutable audit"]:
            self.assertIn(phrase, description)

    def test_new_manual_and_purchase_bases_are_machine_discriminated_without_repricing_retained_source(self):
        for value in [0, MAXIMUM]:
            validator("AssetWrite").validate({**PROFILE, "manual_acquisition_minor": value})
        for patch in [{"manual_acquisition_minor": None}, {"cost_basis": "purchase"}, {"cost_basis": "unknown"}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("AssetWrite").validate({**PROFILE, **patch})
        # Legacy response representation is not proof of today's financial eligibility.
        for name, resource in [("Asset", ASSET), ("Cost", COST), ("Disposal", DISPOSAL)]:
            validator(name).validate({**resource, "currency": "ZZZ"})
        validator("Usage").validate({**USAGE, "event_type": "correction", "delta": -1})
        validator("Cost").validate({**COST, "kind": "adjustment"})
        validator("Disposal").validate({**DISPOSAL, "kind": "donated"})

    def test_usage_corrections_are_new_nonzero_deltas_with_a_real_original_reference(self):
        validator("UsageWrite").validate(USE)
        validator("UsageWrite").validate({**USE, "correction_of": None})
        validator("UsageWrite").validate({**USE, "event_type": "correction", "delta": -MAXIMUM, "correction_of": "😀" * 128})
        for patch in [{"delta": 0}, {"delta": -1}, {"correction_of": "use"}, {"event_type": "correction"},
                      {"event_type": "correction", "correction_of": None},
                      {"event_type": "correction", "correction_of": ""},
                      {"event_type": "correction", "correction_of": "😀" * 129}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("UsageWrite").validate({**USE, **patch})
        for phrase in ["same asset", "running total", "immutable", "not a replacement", "same exact instant", "UUID ordering"]:
            self.assertIn(phrase, SCHEMAS["UsageWrite"]["description"])

    def test_cost_adjustments_require_a_reference_but_real_refunds_remain_distinct(self):
        validator("CostWrite").validate(EXPENSE)
        validator("CostWrite").validate({**EXPENSE, "kind": "refund", "direction": "credit"})
        validator("CostWrite").validate({**EXPENSE, "kind": "adjustment", "direction": "credit", "correction_of": "cost"})
        validator("CostWrite").validate({**EXPENSE, "kind": "adjustment", "correction_of": "cost"})
        for patch in [{"amount_minor": 0}, {"kind": "adjustment"}, {"kind": "adjustment", "correction_of": None},
                      {"kind": "adjustment", "correction_of": ""}, {"kind": "adjustment", "correction_of": "😀" * 129},
                      {"correction_of": "cost"}, {"kind": "refund", "correction_of": "cost"}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("CostWrite").validate({**EXPENSE, **patch})
        for phrase in ["same asset", "currency", "signed", "immutable"]:
            self.assertIn(phrase, SCHEMAS["CostWrite"]["description"])

    def test_append_events_do_not_confuse_equal_values_with_original_key_reconciliation(self):
        for path in ["/units/{id}/usage", "/units/{id}/costs"]:
            description = API["paths"][path]["post"]["description"]
            for phrase in ["fresh key records another event", "even if every field matches", "original-key replay"]:
                self.assertIn(phrase, description)

    def test_lifecycle_writes_bind_original_unit_separately_from_the_asset_proof(self):
        for name, body in [("UnitRetireInput", RETIRE), ("DisposalWrite", DISPOSE)]:
            validator(name).validate(body)
            for field in ["unit_revision", "retired_at" if name == "UnitRetireInput" else "realized_proceeds_minor"]:
                with self.subTest(schema=name, missing=field), self.assertRaises(ValidationError):
                    validator(name).validate({key: value for key, value in body.items() if key != field})
            for value in [None, 0, -1, True, "1", 1.5, MAXIMUM + 1]:
                with self.subTest(schema=name, unit_revision=value), self.assertRaises(ValidationError):
                    validator(name).validate({**body, "unit_revision": value})
            self.assertIn("original Unit", SCHEMAS[name]["description"])
        for kind in ["sold", "donated", "recycled", "discarded", "other"]:
            validator("DisposalWrite").validate({**DISPOSE, "kind": kind, "realized_proceeds_minor": 0})
            if kind != "sold":
                with self.subTest(kind=kind), self.assertRaises(ValidationError):
                    validator("DisposalWrite").validate({**DISPOSE, "kind": kind})
        for field in ["in_service_at", "retired_at", "location_id", "estimated_residual_minor"]:
            with self.subTest(extra=field), self.assertRaises(ValidationError):
                validator("DisposalWrite").validate({**DISPOSE, field: None})

    def test_safe_money_usage_and_revisions_never_accept_boolean_rounding_or_display_text(self):
        for name, body, field, zero in [("AssetWrite", PROFILE, "manual_acquisition_minor", True),
                                        ("AssetWrite", PROFILE, "estimated_residual_minor", True),
                                        ("CostWrite", EXPENSE, "amount_minor", False),
                                        ("DisposalWrite", DISPOSE, "realized_proceeds_minor", True),
                                        ("UsageWrite", USE, "delta", False)]:
            validator(name).validate({**body, field: MAXIMUM})
            for value in [-1, 0.5, True, "1000", MAXIMUM + 1] + ([] if zero else [0]):
                with self.subTest(schema=name, field=field, value=value), self.assertRaises(ValidationError):
                    validator(name).validate({**body, field: value})
            if zero:
                validator(name).validate({**body, field: 0})

    def test_financial_instants_share_lossless_bounds_without_changing_unrelated_aliases(self):
        cases = [("Asset", ASSET, "in_service_at"), ("Asset", ASSET, "retired_at"),
                 ("AssetWrite", PROFILE, "in_service_at"), ("AssetWrite", PROFILE, "retired_at"),
                 ("Usage", USAGE, "occurred_at"), ("UsageWrite", USE, "occurred_at"),
                 ("Cost", COST, "occurred_at"), ("CostWrite", EXPENSE, "occurred_at"),
                 ("Disposal", DISPOSAL, "disposed_at"), ("DisposalWrite", DISPOSE, "disposed_at"),
                 ("UnitRetireInput", RETIRE, "retired_at"), ("AssetCostSummary", SUMMARY, "as_of")]
        for name, body, field in cases:
            for value in ["0001-01-01T00:00:00Z", INSTANT, "9999-12-31T23:59:59.999999999Z"]:
                validator(name).validate({**body, field: value})
            for value in ["", "2026-11-07", "2026-11-07t00:00:00z", "2026-11-07T0:00:00Z", "2026-11-07 00:00:00Z",
                          "2026-11-07T00:00:00,1Z", "2026-11-07T00:00:60Z", "2026-11-07T00:00:00.1234567890Z",
                          "2026-11-07T00:00:00+24:00", "2026-11-07T00:00:00+00:60", "2026-02-30T00:00:00Z",
                          "0000-01-01T00:00:00Z", "10000-01-01T00:00:00Z", "2026-11-07T00:00:00Z\n"]:
                with self.subTest(schema=name, field=field, value=value), self.assertRaises(ValidationError):
                    validator(name).validate({**body, field: value})
        self.assertEqual({"type": "string", "minLength": 1, "maxLength": 64, "format": "date-time"}, SCHEMAS["AIJob"]["properties"]["created_at"])
        self.assertEqual({"type": "string", "format": "date-time"}, SCHEMAS["AnalyticsGroups"]["properties"]["as_of"])

    def test_text_currency_and_usage_units_are_exact_bounded_representations(self):
        for name, body in [("UsageWrite", USE), ("CostWrite", EXPENSE), ("DisposalWrite", DISPOSE), ("UnitRetireInput", RETIRE)]:
            validator(name).validate({**body, "note": "  Exact é\n\x00  "})
            validator(name).validate({**body, "note": "😀" * 10000})
            for value in [None, "😀" * 10001, 1]:
                with self.subTest(schema=name, note_type=type(value).__name__), self.assertRaises(ValidationError):
                    validator(name).validate({**body, "note": value})
        for name, body in [("Asset", ASSET), ("AssetWrite", PROFILE), ("Cost", COST), ("CostWrite", EXPENSE),
                           ("Disposal", DISPOSAL), ("DisposalWrite", DISPOSE)]:
            for value in ["usd", "JP", "JPYX", "JPY\n", "日圓幣", None]:
                with self.subTest(schema=name, currency=value), self.assertRaises(ValidationError):
                    validator(name).validate({**body, "currency": value})
        for value in ["times", "minutes", "cycles"]:
            validator("AssetWrite").validate({**PROFILE, "usage_unit": value})
        with self.assertRaises(ValidationError):
            validator("AssetWrite").validate({**PROFILE, "usage_unit": "hours"})

    def test_live_pages_have_required_terminal_cursors_and_no_list_etag(self):
        for path, name, resource in PAGES:
            validator(name).validate({"data": [], "next_cursor": None})
            validator(name).validate({"data": [deepcopy(resource) for _ in range(100)], "next_cursor": "x" * 2048})
            for body in [{"data": []}, {"data": [], "next_cursor": ""}, {"data": [], "next_cursor": "x" * 2049},
                         {"data": [deepcopy(resource) for _ in range(101)], "next_cursor": None}]:
                with self.subTest(schema=name, fields=tuple(body)), self.assertRaises(ValidationError):
                    validator(name).validate(body)
            operation = API["paths"][path]["get"]
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(16384, operation["x-max-query-bytes"])
            self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
            self.assertEqual({"type": "integer", "minimum": 1, "maximum": 100, "default": 50}, parameter(path, "get", "limit")["schema"])
            self.assertEqual({"type": "string", "minLength": 1, "maxLength": 2048}, parameter(path, "get", "cursor")["schema"])

    def test_foreground_writes_have_exact_admission_and_independent_revision_scopes(self):
        for path, method, proof, response in WRITES:
            operation = API["paths"][path][method]
            headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
            self.assertEqual({"Origin", "X-CSRF-Token", "Idempotency-Key", "X-HomeBox-Client-Version", "If-Match"}, set(headers))
            self.assertTrue(all(entry["required"] for entry in headers.values()))
            self.assertEqual(2048, headers["Origin"]["schema"]["maxLength"])
            self.assertEqual(proof, operation["x-revision-scope"])
            self.assertEqual(response, operation["x-response-revision-scope"])
            self.assertEqual(131072, operation["x-max-body-bytes"])
            self.assertEqual(0, operation["x-max-query-bytes"])
            self.assertEqual("application/json", operation["x-content-type"])
            self.check_strong_proof(headers["If-Match"]["schema"])
            self.check_strong_proof(operation["responses"]["200"]["headers"]["ETag"]["schema"])
            for phrase in ["original", "atomically", "reconciliation"]:
                self.assertIn(phrase, operation["description"])

    def check_strong_proof(self, schema):
        check = Draft202012Validator(schema)
        for value in ['"1"', f'"{MAXIMUM}"']:
            check.validate(value)
        for value in ['1', '"0"', '"01"', 'W/"1"', '*', '"1", "2"', '"1.0"', '"1e3"', '"1"\n', '"12345678901234567"']:
            with self.subTest(proof=value), self.assertRaises(ValidationError):
                check.validate(value)

    def test_detail_reads_are_bodyless_and_recover_real_disposal_identity(self):
        for path in READS:
            operation = API["paths"][path]["get"]
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertNotIn("requestBody", operation)
            self.assertNotEqual([], operation.get("security", API["security"]))
            self.assertEqual("no-store", operation["responses"]["200"]["headers"]["Cache-Control"]["schema"]["const"])
        for path in ["/units/{id}/asset", "/units/{id}/disposal"]:
            self.assertEqual(0, API["paths"][path]["get"]["x-max-query-bytes"])
            self.check_strong_proof(API["paths"][path]["get"]["responses"]["200"]["headers"]["ETag"]["schema"])
        self.assertEqual("get_units_id_disposal", API["paths"]["/units/{id}/disposal"]["get"]["operationId"])
        self.assertEqual({"$ref": "#/components/schemas/Disposal"}, API["paths"]["/units/{id}/disposal"]["get"]["responses"]["200"]["content"]["application/json"]["schema"])

    def test_asset_filters_use_that_exact_unit_and_preserve_complete_civil_scope(self):
        operation = API["paths"]["/assets"]["get"]
        params = {entry["name"]: entry["schema"] for entry in operation["parameters"]}
        self.assertEqual({"limit", "cursor", "from", "to", "currency", "category_id", "location_id", "scope", "status", "merchant"}, set(params))
        self.assertEqual(["active", "archived"], params["status"]["enum"])
        for field in ["from", "to"]:
            check = Draft202012Validator(params[field], format_checker=FORMAT)
            for value in ["0001-01-01", "2024-02-29", "9999-12-31"]:
                check.validate(value)
            for value in ["0000-01-01", "2026-02-30", "2026-2-01", "2026-11-07\n"]:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    check.validate(value)
        for phrase in ["exact Unit", "same purchase", "home timezone", "before slicing", "current placement", "not frozen snapshots"]:
            self.assertIn(phrase, operation["description"])

    def test_summary_keeps_negative_realized_and_nullable_projected_divisors(self):
        validator("AssetCostSummary").validate(SUMMARY)
        self.assertEqual(set(SUMMARY), set(SCHEMAS["AssetCostSummary"]["required"]))
        validator("AssetCostSummary").validate({**SUMMARY, "extra_minor": -MAXIMUM, "net_cost_minor": -MAXIMUM,
                                               "estimated_residual_minor": None, "projected_net_cost_minor": None,
                                               "elapsed_days": None, "total_usage": 0, "per_day_minor": None,
                                               "per_use_minor": None, "projected_per_day_minor": None, "projected_per_use_minor": None})
        for field in ["net_cost_minor", "extra_minor", "projected_net_cost_minor"]:
            for value in [-MAXIMUM - 1, MAXIMUM + 1, 0.5, True, "0"]:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    validator("AssetCostSummary").validate({**SUMMARY, field: value})
        for patch in [{"elapsed_days": 0}, {"total_usage": -1}, {"source_revision": -1}, {"source_revision": MAXIMUM + 1},
                      {"source_revision": True}, {"timezone": ""}, {"day_policy": "24_hour_durations"},
                      {"elapsed_days": None}, {"total_usage": 0}, {"estimated_residual_minor": None},
                      {"projected_net_cost_minor": None}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("AssetCostSummary").validate({**SUMMARY, **patch})
        description = SCHEMAS["AssetCostSummary"]["description"]
        for phrase in ["current posted", "not a historical", "not depreciation", "never clamp", "inclusive civil", "never subtract both"]:
            self.assertIn(phrase, description)
        operation = API["paths"]["/units/{id}/cost-summary"]["get"]
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        self.assertEqual(16384, operation["x-max-query-bytes"])
        self.assertEqual(SCHEMAS["AssetCostSummary"]["properties"]["as_of"], parameter("/units/{id}/cost-summary", "get", "as_of")["schema"])

    def test_immutable_offline_payloads_keep_original_identity_and_separate_revision_envelopes(self):
        for _, resource, kind in RESOURCES:
            payload = {key: value for key, value in resource.items() if key != "revision"}
            identity = resource.get("id", resource["item_unit_id"])
            Draft202012Validator(PROJECTION).validate(record(kind, payload, identity=identity))
            Draft202012Validator(PROJECTION).validate(record(kind, operation="delete", identity=identity))
            self.assertEqual(set(payload), set(PROJECTION["$defs"][kind]["properties"]))
            for private in ["revision", "item_revision", "unit_revision", "asset_revision", "mutation_event_id", "idempotency_key", "created_at"]:
                self.assertNotIn(private, PROJECTION["$defs"][kind]["properties"])
                with self.subTest(kind=kind, extra=private), self.assertRaises(ValidationError):
                    Draft202012Validator(PROJECTION).validate(record(kind, {**payload, private: 1}, identity=identity))

    def test_existing_operation_identities_and_no_destructive_event_routes_are_preserved(self):
        self.assertEqual(11, len(READS) + len(WRITES))
        for path, method in [(path, "get") for path in READS] + [(path, method) for path, method, _, _ in WRITES]:
            operation = API["paths"][path][method]
            self.assertEqual(method + "_" + path.strip("/").replace("{", "").replace("}", "").replace("/", "_").replace("-", "_"), operation["operationId"])
        for path in ["/units/{id}/usage", "/units/{id}/costs", "/units/{id}/disposal", "/units/{id}/asset"]:
            self.assertNotIn("delete", API["paths"][path])
            self.assertNotIn("patch", API["paths"][path])
        self.assertNotIn("/usage/{id}", API["paths"])
        self.assertNotIn("/costs/{id}", API["paths"])
