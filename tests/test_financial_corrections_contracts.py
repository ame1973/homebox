"""Financial correction schemas; these are not service or physical acceptance."""
import unittest

from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, PROJECTION, SCHEMAS, validator


MAXIMUM = 9007199254740991
INSTANT = "2026-11-07T02:00:00.123456789Z"
LINE = {"id": "line", "revision": 9, "purchase_id": "purchase", "item_id": "item",
        "item_unit_id": "unit", "quantity_base": 3, "paid_minor": 100, "note": "Retained allocation"}
REPLACEMENT = {"item_id": "other-item", "item_revision": 17, "item_unit_id": None,
               "quantity_base": 2, "paid_minor": 0}
LINE_VOID = {"line_id": "line", "line_revision": 9, "item_revision": 11,
             "action": "void", "replacement": None, "reason": "Void the incorrect attribution"}
LINE_REPLACE = {**LINE_VOID, "action": "replace", "replacement": REPLACEMENT}
AFTER_LINE = {"id": "new-line", "revision": 1, "purchase_id": "purchase", "item_id": "other-item",
              "item_unit_id": None, "quantity_base": 2, "paid_minor": 0, "note": ""}
LINE_ENTRY = {"id": "line-correction", "revision": 1, "purchase_id": "purchase", "action": "replace",
              "reason": "Correct the attribution", "created_at": INSTANT, "before": LINE, "after": AFTER_LINE}
DISPOSAL = {"item_unit_id": "unit", "disposed_at": INSTANT, "kind": "sold",
            "realized_proceeds_minor": 200, "currency": "JPY", "note": "Original decision"}
RECORD = {"item_unit_id": "unit", "revision": 7, "original": DISPOSAL, "current": DISPOSAL}
VOIDED_RECORD = {**RECORD, "revision": 8, "current": None}
FINANCE = {key: value for key, value in DISPOSAL.items() if key not in {"item_unit_id", "note"}}
DISPOSAL_VOID = {"asset_revision": 13, "action": "void", "replacement": None,
                 "reason": "Void only the financial effect"}
DISPOSAL_REPLACE = {**DISPOSAL_VOID, "action": "replace", "replacement": {**FINANCE, "realized_proceeds_minor": 0}}
DISPOSAL_ENTRY = {"id": "disposal-correction", "revision": 1, "item_unit_id": "unit", "action": "void",
                  "reason": "Void only the financial effect", "created_at": INSTANT,
                  "before": RECORD, "after": VOIDED_RECORD}
WRITES = [("/purchases/{id}/line-corrections", "PurchaseLineCorrectionWrite", "PurchaseLineCorrection", "purchase", "purchase_line_correction"),
          ("/units/{id}/disposal-corrections", "DisposalCorrectionWrite", "DisposalCorrection", "disposal", "disposal_correction")]


class FinancialCorrectionContractTests(unittest.TestCase):
    def reject(self, name, *values):
        for value in values:
            with self.subTest(schema=name, value=value), self.assertRaises(ValidationError):
                validator(name).validate(value)

    def test_five_additional_identities_do_not_add_destructive_posted_row_routes(self):
        operations = {(method, path): operation["operationId"] for path, methods in API["paths"].items()
                      for method, operation in methods.items()}
        for method, path, identity in [
            ("get", "/purchases/{id}/line-corrections", "get_purchases_id_line_corrections"),
            ("post", "/purchases/{id}/line-corrections", "post_purchases_id_line_corrections"),
            ("get", "/units/{id}/disposal-record", "get_units_id_disposal_record"),
            ("get", "/units/{id}/disposal-corrections", "get_units_id_disposal_corrections"),
            ("post", "/units/{id}/disposal-corrections", "post_units_id_disposal_corrections"),
        ]:
            self.assertEqual(identity, operations[method, path])
        self.assertEqual({"get", "post"}, set(API["paths"]["/purchases/{id}/lines"]))
        self.assertEqual({"get", "post"}, set(API["paths"]["/units/{id}/disposal"]))
        self.assertEqual({"get"}, set(API["paths"]["/units/{id}/disposal-record"]))
        self.assertNotIn("/purchase-lines/{id}", API["paths"])

    def test_protected_corrections_have_exact_admission_and_distinct_original_subject_proofs(self):
        for path, body, response, scope, reply_scope in WRITES:
            operation = API["paths"][path]["post"]
            headers = {value["name"]: value for value in operation["parameters"] if value["in"] == "header"}
            self.assertEqual({"Origin", "X-CSRF-Token", "Idempotency-Key", "X-HomeBox-Client-Version", "If-Match"}, set(headers))
            self.assertTrue(all(value["required"] for value in headers.values()))
            self.assertEqual('^"[1-9][0-9]{0,15}"(?![\\s\\S])', headers["If-Match"]["schema"]["pattern"])
            self.assertEqual(131072, operation["x-max-body-bytes"])
            self.assertEqual(0, operation["x-max-query-bytes"])
            self.assertEqual(scope, operation["x-revision-scope"])
            self.assertEqual(reply_scope, operation["x-response-revision-scope"])
            self.assertEqual({"application/json"}, set(operation["requestBody"]["content"]))
            self.assertTrue(operation["requestBody"]["required"])
            self.assertEqual(f"#/components/schemas/{body}", operation["requestBody"]["content"]["application/json"]["schema"]["$ref"])
            success = operation["responses"]["200"]
            self.assertEqual(f"#/components/schemas/{response}", success["content"]["application/json"]["schema"]["$ref"])
            self.assertEqual({"Cache-Control", "ETag"}, set(success["headers"]))
            self.assertEqual("no-store", success["headers"]["Cache-Control"]["schema"]["const"])
            self.assertTrue({"400", "401", "403", "404", "409", "413", "415", "422", "426", "429", "503"}.issubset(operation["responses"]))
            for phrase in ["original", "inside SQL", "128 KiB", "replay", "outbox", "audit", "frozen", "physical"]:
                self.assertIn(phrase, operation["description"])

    def test_purchase_void_and_replacement_are_complete_explicit_decisions(self):
        validator("PurchaseLineCorrectionWrite").validate(LINE_VOID)
        validator("PurchaseLineCorrectionWrite").validate(LINE_REPLACE)
        for field in LINE_VOID:
            self.reject("PurchaseLineCorrectionWrite", {key: value for key, value in LINE_VOID.items() if key != field})
        for patch in [{"action": "replace"}, {"replacement": REPLACEMENT}, {"action": "delete"},
                      {"replacement": {}}, {"replacement": "null"}, {"replacement": False}]:
            self.reject("PurchaseLineCorrectionWrite", {**LINE_VOID, **patch})
        for name in ["paid_minor", "quantity_base", "unit_revision", "asset_revision", "source_revision", "purchase_id", "idempotency_key"]:
            self.reject("PurchaseLineCorrectionWrite", {**LINE_VOID, name: 1})
        for phrase in ["original Purchase", "original Line", "owning Item", "new independent Line", "tombstone", "Active zero-paid", "negative physical"]:
            self.assertIn(phrase, SCHEMAS["PurchaseLineCorrectionWrite"]["description"])

    def test_replacement_reuses_actual_original_item_and_explicit_nullable_one_unit_intent(self):
        for target in [REPLACEMENT, {**REPLACEMENT, "item_unit_id": "other-unit", "quantity_base": 1}]:
            validator("PurchaseLineCorrectionWrite").validate({**LINE_REPLACE, "replacement": target})
        for field in ["item_id", "item_revision", "item_unit_id", "quantity_base", "paid_minor"]:
            self.reject("PurchaseLineCorrectionWrite", {**LINE_REPLACE, "replacement": {key: value for key, value in REPLACEMENT.items() if key != field}})
        for patch in [{"item_unit_id": "unit", "quantity_base": 2}, {"item_revision": None}, {"expectedRevision": 11},
                      {"paid_minor": -1}, {"quantity_base": 0}, {"item_unit_id": {"value": None}}]:
            self.reject("PurchaseLineCorrectionWrite", {**LINE_REPLACE, "replacement": {**REPLACEMENT, **patch}})

    def test_original_body_proofs_are_exact_positive_safe_integers_not_other_resource_metadata(self):
        for name, body, fields in [("PurchaseLineCorrectionWrite", LINE_VOID, ["line_revision", "item_revision"]),
                                   ("DisposalCorrectionWrite", DISPOSAL_VOID, ["asset_revision"])]:
            for field in fields:
                validator(name).validate({**body, field: MAXIMUM})
                for value in [0, -1, MAXIMUM + 1, 1.5, True, "7", None]:
                    self.reject(name, {**body, field: value})
        for value in ["", "😀" * 129, None, 1, {"id": "line"}]:
            self.reject("PurchaseLineCorrectionWrite", {**LINE_VOID, "line_id": value})
        validator("PurchaseLineCorrectionWrite").validate({**LINE_VOID, "line_id": "😀" * 128})

    def test_reason_is_required_nonempty_bounded_exact_text_for_commands_and_history(self):
        for name, body in [("PurchaseLineCorrectionWrite", LINE_VOID), ("DisposalCorrectionWrite", DISPOSAL_VOID),
                           ("PurchaseLineCorrection", LINE_ENTRY), ("DisposalCorrection", DISPOSAL_ENTRY)]:
            validator(name).validate({**body, "reason": "\0Exact é\n"})
            validator(name).validate({**body, "reason": "😀" * 10000})
            for value in ["", "😀" * 10001, None, 1, True, ["reason"]]:
                self.reject(name, {**body, "reason": value})
            self.reject(name, {key: value for key, value in body.items() if key != "reason"})

    def test_retained_disposal_record_separates_original_current_null_and_known_zero(self):
        validator("DisposalRecord").validate(RECORD)
        validator("DisposalRecord").validate(VOIDED_RECORD)
        validator("DisposalRecord").validate({**RECORD, "current": {**DISPOSAL, "realized_proceeds_minor": 0}})
        for field in RECORD:
            self.reject("DisposalRecord", {key: value for key, value in RECORD.items() if key != field})
        for patch in [{"original": None}, {"original": {}}, {"current": {}}, {"current": False},
                      {"revision": 0}, {"revision": MAXIMUM + 1}, {"source_revision": 7}, {"unit_revision": 7}]:
            self.reject("DisposalRecord", {**RECORD, **patch})
        operation = API["paths"]["/units/{id}/disposal-record"]["get"]
        self.assertNotIn("requestBody", operation)
        self.assertEqual(0, operation["x-max-body-bytes"])
        self.assertEqual(0, operation["x-max-query-bytes"])
        self.assertEqual(["id"], [value["name"] for value in operation["parameters"]])
        self.assertEqual("disposal", operation["x-response-revision-scope"])
        self.assertEqual({"Cache-Control", "ETag"}, set(operation["responses"]["200"]["headers"]))
        for phrase in ["after a void", "actual", "404", "counter", "not", "proof"]:
            self.assertIn(phrase, operation["description"])

    def test_bounded_disposal_values_preserve_legacy_representation_without_repricing(self):
        validator("DisposalData").validate(DISPOSAL)
        validator("DisposalData").validate({**DISPOSAL, "kind": "donated", "currency": "ZZZ"})
        validator("DisposalData").validate({**DISPOSAL, "item_unit_id": "😀" * 128, "realized_proceeds_minor": MAXIMUM, "note": "😀" * 10000})
        for field in DISPOSAL:
            self.reject("DisposalData", {key: value for key, value in DISPOSAL.items() if key != field})
        for patch in [{"item_unit_id": "😀" * 129}, {"realized_proceeds_minor": MAXIMUM + 1}, {"realized_proceeds_minor": True},
                      {"realized_proceeds_minor": -1}, {"note": "😀" * 10001}, {"revision": 1}, {"asset_revision": 1}]:
            self.reject("DisposalData", {**DISPOSAL, **patch})
        self.assertEqual(set(SCHEMAS["Disposal"]["properties"]) - {"revision"}, set(SCHEMAS["DisposalData"]["properties"]))

    def test_disposal_corrections_use_asset_body_proof_and_explicit_nullable_financial_intent_only(self):
        validator("DisposalCorrectionWrite").validate(DISPOSAL_VOID)
        validator("DisposalCorrectionWrite").validate(DISPOSAL_REPLACE)
        for field in DISPOSAL_VOID:
            self.reject("DisposalCorrectionWrite", {key: value for key, value in DISPOSAL_VOID.items() if key != field})
        for patch in [{"action": "replace"}, {"replacement": FINANCE}, {"action": "delete"}, {"replacement": {}},
                      {"replacement": {"$write": None}}, {"unit_revision": 13}, {"item_revision": 11}, {"source_revision": 42}]:
            self.reject("DisposalCorrectionWrite", {**DISPOSAL_VOID, **patch})
        for phrase in ["original retained DisposalRecord", "original Asset", "reinstate", "physical movement", "Unit revival", "location/search", "original-key replay"]:
            self.assertIn(phrase, SCHEMAS["DisposalCorrectionWrite"]["description"])

    def test_financial_replacement_requires_date_currency_kind_and_deliberate_zero_proceeds(self):
        for kind in ["sold", "donated", "recycled", "discarded", "other"]:
            validator("DisposalFinancialWrite").validate({**FINANCE, "kind": kind, "realized_proceeds_minor": 0})
            if kind != "sold":
                self.reject("DisposalFinancialWrite", {**FINANCE, "kind": kind})
        for field in ["kind", "currency", "disposed_at", "realized_proceeds_minor"]:
            self.reject("DisposalFinancialWrite", {key: value for key, value in FINANCE.items() if key != field})
        for name in ["unit_revision", "item_revision", "asset_revision", "in_service_at", "retired_at", "location_id", "estimated_residual_minor"]:
            self.reject("DisposalFinancialWrite", {**FINANCE, name: None})
        validator("DisposalFinancialWrite").validate({**FINANCE, "note": ""})
        self.reject("DisposalFinancialWrite", {**FINANCE, "note": None})

    def test_new_money_and_counts_do_not_accept_negative_fractional_boolean_or_display_values(self):
        for name, body, field in [("DisposalFinancialWrite", FINANCE, "realized_proceeds_minor"),
                                   ("PurchaseLineWrite", REPLACEMENT, "paid_minor")]:
            for value in [0, MAXIMUM]:
                validator(name).validate({**body, field: value})
            for value in [-1, MAXIMUM + 1, 1.5, True, "0", "1,000", None]:
                self.reject(name, {**body, field: value})
        validator("PurchaseLineWrite").validate({**REPLACEMENT, "quantity_base": MAXIMUM})
        self.reject("PurchaseLineWrite", {**REPLACEMENT, "quantity_base": MAXIMUM + 1})

    def test_financial_and_actual_history_instants_are_bounded_without_guessing_old_creation(self):
        for value in [INSTANT, "2026-11-07T10:00:00.123456789+08:00", "0001-01-01T00:00:00Z"]:
            validator("DisposalFinancialWrite").validate({**FINANCE, "disposed_at": value})
            validator("PurchaseLineCorrection").validate({**LINE_ENTRY, "created_at": value})
        for value in ["2026-11-07", "2026-11-07t02:00:00z", "2026-11-07T02:00:00.1234567890Z",
                      "2026-02-30T00:00:00Z", "2026-11-07T24:00:00Z", "2026-11-07T00:00:00Z\n", "", None, 1]:
            self.reject("DisposalFinancialWrite", {**FINANCE, "disposed_at": value})
            self.reject("PurchaseLineCorrection", {**LINE_ENTRY, "created_at": value})
        self.assertNotIn("created_at", SCHEMAS["PurchaseLine"]["properties"])

    def test_purchase_history_has_exact_original_and_new_independent_replacement_or_null(self):
        validator("PurchaseLineCorrection").validate(LINE_ENTRY)
        validator("PurchaseLineCorrection").validate({**LINE_ENTRY, "action": "void", "after": None})
        for field in LINE_ENTRY:
            self.reject("PurchaseLineCorrection", {key: value for key, value in LINE_ENTRY.items() if key != field})
        for patch in [{"action": "void"}, {"action": "replace", "after": None}, {"before": None},
                      {"before": {"id": "line"}}, {"after": {**AFTER_LINE, "revision": 2}}, {"revision": 2},
                      {"revision": 0}, {"id": ""}, {"id": "😀" * 129}, {"raw_request": {"private": True}}]:
            self.reject("PurchaseLineCorrection", {**LINE_ENTRY, **patch})

    def test_disposal_history_retains_both_whole_records_and_discriminates_void_replacement(self):
        validator("DisposalCorrection").validate(DISPOSAL_ENTRY)
        replacement = {**VOIDED_RECORD, "revision": 9, "current": {**DISPOSAL, "realized_proceeds_minor": 0}}
        validator("DisposalCorrection").validate({**DISPOSAL_ENTRY, "action": "replace", "before": VOIDED_RECORD, "after": replacement})
        for field in DISPOSAL_ENTRY:
            self.reject("DisposalCorrection", {key: value for key, value in DISPOSAL_ENTRY.items() if key != field})
        for patch in [{"action": "replace"}, {"after": RECORD}, {"before": None}, {"after": None},
                      {"before": {}}, {"revision": 2}, {"unit_revision": 13}, {"body": DISPOSAL_VOID}]:
            self.reject("DisposalCorrection", {**DISPOSAL_ENTRY, **patch})

    def test_history_pages_have_required_null_continuations_real_bounded_rows_and_no_list_etag(self):
        for path, name, entry in [(WRITES[0][0], "PurchaseLineCorrectionList", LINE_ENTRY),
                                   (WRITES[1][0], "DisposalCorrectionList", DISPOSAL_ENTRY)]:
            validator(name).validate({"data": [], "next_cursor": None})
            validator(name).validate({"data": [entry] * 100, "next_cursor": "x" * 2048})
            for value in [{"data": []}, {"data": [entry] * 101, "next_cursor": None},
                          {"data": [], "next_cursor": "x" * 2049}, {"data": [], "next_cursor": ""},
                          {"data": [{"id": "entry"}], "next_cursor": None}, {"data": [], "next_cursor": None, "revision": 7}]:
                self.reject(name, value)
            operation = API["paths"][path]["get"]
            parameters = {value["name"]: value for value in operation["parameters"]}
            self.assertEqual({"id", "limit", "cursor"}, set(parameters))
            self.assertEqual({"type": "integer", "minimum": 1, "maximum": 100, "default": 50}, parameters["limit"]["schema"])
            self.assertEqual(2048, parameters["cursor"]["schema"]["maxLength"])
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(16384, operation["x-max-query-bytes"])
            self.assertNotIn("requestBody", operation)
            self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
            for phrase in ["owner", "principal", "epoch", "lookahead", "live", "15 minutes"]:
                self.assertIn(phrase, operation["description"])

    def test_22_original_offline_shapes_remain_current_read_projections_not_a_private_audit_ledger(self):
        self.assertEqual(22, len(PROJECTION["$defs"]))
        self.assertEqual({"id", "purchase_id", "item_id", "item_unit_id", "quantity_base", "paid_minor", "note"},
                         set(PROJECTION["$defs"]["purchase_line"]["properties"]))
        self.assertEqual(set(DISPOSAL), set(PROJECTION["$defs"]["disposal"]["properties"]))
        self.assertNotIn("purchase_line_correction", PROJECTION["$defs"])
        self.assertNotIn("disposal_correction", PROJECTION["$defs"])
        for path, method in [("/purchases/{id}/lines", "get"), ("/units/{id}/disposal", "get")]:
            self.assertIn("void", API["paths"][path][method]["description"])
        self.assertIn("correction source", API["paths"]["/units/{id}/cost-summary"]["get"]["description"])

    def test_schema_acceptance_does_not_claim_owner_revision_progression_or_financial_eligibility(self):
        structurally_valid = {**LINE_ENTRY, "after": {**AFTER_LINE, "id": LINE["id"], "purchase_id": "foreign"}}
        validator("PurchaseLineCorrection").validate(structurally_valid)
        validator("DisposalRecord").validate({**RECORD, "current": {**DISPOSAL, "item_unit_id": "foreign"}})
        validator("DisposalFinancialWrite").validate({**FINANCE, "currency": "ZZZ"})
        self.assertIn("runtime validation", SCHEMAS["PurchaseLineCorrection"]["description"])
        self.assertIn("runtime validation", SCHEMAS["DisposalCorrection"]["description"])
        self.assertIn("actual", SCHEMAS["DisposalRecord"]["description"])
        self.assertIn("finite", SCHEMAS["DisposalFinancialWrite"]["description"])
