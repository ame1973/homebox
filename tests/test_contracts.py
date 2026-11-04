"""Executable contract checks; these do not claim HTTP implementation coverage."""
from copy import deepcopy
import json
from pathlib import Path
import re
import unittest

from jsonschema import Draft202012Validator, FormatChecker
from jsonschema.exceptions import ValidationError
from openapi_spec_validator import OpenAPIV31SpecValidator
import yaml

ROOT = Path(__file__).resolve().parents[1]
API = yaml.safe_load((ROOT / "contracts/openapi.yaml").read_text())
PROJECTION = json.loads((ROOT / "contracts/offline-projection.schema.json").read_text())
SCHEMAS = API["components"]["schemas"]
FORMAT = FormatChecker()


def resolve(value):
    if "$ref" not in value:
        return value
    target = API
    for part in value["$ref"][2:].split("/"):
        target = target[part.replace("~1", "/").replace("~0", "~")]
    return target


def validator(name):
    return Draft202012Validator(
        {"$ref": f"#/components/schemas/{name}", "components": API["components"]},
        format_checker=FORMAT,
    )


def example(schema):
    """Build a deterministic minimal fixture for standalone offline definitions."""
    if "const" in schema:
        return schema["const"]
    if "enum" in schema:
        return schema["enum"][0]
    if "anyOf" in schema:
        options = schema["anyOf"]
        return None if any(option.get("type") == "null" for option in options) else example(options[0])
    kind = schema.get("type")
    if isinstance(kind, list):
        return None if "null" in kind else example({**schema, "type": kind[0]})
    if kind == "object":
        return {key: example(schema["properties"][key]) for key in schema.get("required", [])}
    if kind == "integer":
        return max(1, schema.get("minimum", 0))
    if kind == "boolean":
        return False
    if kind == "array":
        return []
    if schema.get("format") == "date-time":
        return "2026-11-04T00:00:00Z"
    if schema.get("format") == "date":
        return "2026-11-04"
    if schema.get("pattern") == "^[A-Z]{3}$":
        return "JPY"
    if schema.get("pattern") == "^[a-f0-9]{64}$":
        return "a" * 64
    return "x" * max(1, schema.get("minLength", 0))


def record(entity, payload=None, operation="upsert", revision=1, identity="example-id"):
    if payload is None and operation == "upsert":
        payload = example(PROJECTION["$defs"][entity])
    return {"entity_type": entity, "entity_id": identity, "entity_revision": revision,
            "projection_version": 1, "operation": operation, "payload": payload}


class OpenAPIContractTests(unittest.TestCase):
    def test_entire_document_validates_as_openapi_31(self):
        errors = list(OpenAPIV31SpecValidator(API).iter_errors())
        self.assertEqual([], errors)

    def test_all_original_operations_are_preserved(self):
        baseline = json.loads((ROOT / "tests/fixtures/original-operations.json").read_text())
        self.assertEqual(103, len(baseline))
        for method, path in baseline:
            with self.subTest(method=method, path=path):
                self.assertIn(method, API["paths"][path])

    def test_refs_resolve_locally_and_operation_ids_are_unique(self):
        def walk(value):
            if isinstance(value, dict):
                if "$ref" in value:
                    self.assertTrue(value["$ref"].startswith("#/"))
                    target = API
                    for part in value["$ref"][2:].split("/"):
                        target = target[part.replace("~1", "/").replace("~0", "~")]
                for child in value.values():
                    walk(child)
            elif isinstance(value, list):
                for child in value:
                    walk(child)
        walk(API)
        ids = [op["operationId"] for methods in API["paths"].values() for op in methods.values()]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertTrue({"Record", "List", "MutationInput"}.isdisjoint(SCHEMAS))

    def test_every_component_schema_is_valid(self):
        for name, schema in SCHEMAS.items():
            with self.subTest(schema=name):
                Draft202012Validator.check_schema(schema)
                if schema.get("type") == "object":
                    self.assertLessEqual(set(schema.get("required", [])), schema.get("properties", {}).keys())

    def test_only_setup_login_and_liveness_are_public(self):
        public = {(method, path) for path, methods in API["paths"].items()
                  for method, op in methods.items() if op.get("security", API["security"]) == []}
        self.assertEqual({("post", "/auth/login"), ("post", "/auth/setup"), ("get", "/health/live")}, public)

    def test_worker_credentials_are_separate_and_scoped(self):
        for path, methods in API["paths"].items():
            for method, op in methods.items():
                with self.subTest(method=method, path=path):
                    if path.startswith("/worker/"):
                        self.assertEqual([{"WorkerToken": []}], op["security"])
                    else:
                        self.assertNotEqual([{"WorkerToken": []}], op.get("security"))

    def test_protected_writes_have_csrf_idempotency_and_version_headers(self):
        for path, methods in API["paths"].items():
            for method, op in methods.items():
                if method == "get" or path.startswith("/worker/") or op.get("security") == []:
                    continue
                with self.subTest(method=method, path=path):
                    headers = {p["name"] for p in op["parameters"] if p["in"] == "header" and p["required"]}
                    self.assertLessEqual({"X-CSRF-Token", "Idempotency-Key", "X-HomeBox-Client-Version"}, headers)
                    self.assertIn("426", op["responses"])
                    if method in {"patch", "put", "delete"} or path.startswith("/stock/"):
                        self.assertIn("If-Match", headers)

    def test_all_responses_are_network_only_and_errors_are_structured(self):
        for path, methods in API["paths"].items():
            for method, op in methods.items():
                for status, response in op["responses"].items():
                    response = resolve(response)
                    with self.subTest(method=method, path=path, status=status):
                        self.assertEqual("no-store", response["headers"]["Cache-Control"]["schema"]["const"])
                        if int(status) >= 400:
                            self.assertEqual({"$ref": "#/components/schemas/Error"}, response["content"]["application/json"]["schema"])
        self.assertIn("request_id", SCHEMAS["Error"]["properties"]["error"]["required"])

    def test_list_and_sync_page_limits_are_bounded(self):
        for path, methods in API["paths"].items():
            for op in methods.values():
                for parameter in op.get("parameters", []):
                    if parameter["name"] == "limit":
                        with self.subTest(path=path):
                            self.assertGreaterEqual(parameter["schema"]["minimum"], 1)
                            self.assertLessEqual(parameter["schema"]["maximum"], 500)
        for path in ["/sync/bootstrap", "/sync/changes", "/sync/embeddings"]:
            self.assertIn("410", API["paths"][path]["get"]["responses"])
        self.assertIn("delta_cursor", SCHEMAS["BootstrapPage"]["required"])

    def test_paths_bind_every_placeholder(self):
        for path, methods in API["paths"].items():
            for op in methods.values():
                names = {p["name"] for p in op.get("parameters", []) if p["in"] == "path" and p["required"]}
                self.assertEqual(set(re.findall(r"\{([^}]+)\}", path)), names)

    def test_uploads_are_real_multipart_not_unobtainable_tokens(self):
        for path in ["/media", "/imports/upload"]:
            body = API["paths"][path]["post"]["requestBody"]["content"]
            self.assertEqual({"multipart/form-data"}, body.keys())
            name = body["multipart/form-data"]["schema"]["$ref"].split("/")[-1]
            self.assertIn("file", SCHEMAS[name]["required"])
            self.assertEqual("binary", SCHEMAS[name]["properties"]["file"]["format"])

    def test_item_creation_requires_name_but_defaults_to_individual(self):
        name = API["paths"]["/items"]["post"]["requestBody"]["content"]["application/json"]["schema"]["$ref"].split("/")[-1]
        self.assertEqual("ItemCreate", name)
        validator(name).validate({"name": "USB-C cable"})
        self.assertEqual("individual", SCHEMAS[name]["properties"]["tracking_mode"]["default"])
        for payload in [{}, {"name": ""}, {"name": "Cable", "unexpected": True}]:
            with self.subTest(payload=payload), self.assertRaises(ValidationError):
                validator(name).validate(payload)

    def test_media_owner_xor_is_machine_validated(self):
        validator("MediaLinkCreate").validate({"item_id": "item-1"})
        for payload in [{"is_cover": True}, {"item_id": "item-1", "location_id": "location-1"}, {}]:
            with self.subTest(payload=payload), self.assertRaises(ValidationError):
                validator("MediaLinkCreate").validate(payload)

    def test_manual_and_purchase_asset_bases_are_exclusive(self):
        base = {"currency": "JPY", "usage_unit": "times"}
        validator("AssetWrite").validate({**base, "cost_basis": "manual", "manual_acquisition_minor": 120000})
        validator("AssetWrite").validate({**base, "cost_basis": "purchase"})
        for extra in [{"cost_basis": "manual"}, {"cost_basis": "manual", "manual_acquisition_minor": None},
                      {"cost_basis": "purchase", "manual_acquisition_minor": 120000}]:
            with self.subTest(extra=extra), self.assertRaises(ValidationError):
                validator("AssetWrite").validate({**base, **extra})

    def test_usage_correction_requires_reference_and_nonzero_delta(self):
        base = {"occurred_at": "2026-11-04T00:00:00Z", "usage_unit": "times"}
        validator("UsageWrite").validate({**base, "event_type": "use", "delta": 1})
        validator("UsageWrite").validate({**base, "event_type": "correction", "delta": -1, "correction_of": "event-1"})
        for extra in [{"event_type": "use", "delta": 0}, {"event_type": "use", "delta": -1},
                      {"event_type": "correction", "delta": -1}]:
            with self.subTest(extra=extra), self.assertRaises(ValidationError):
                validator("UsageWrite").validate({**base, **extra})

    def test_review_correction_and_advisory_draft_contracts(self):
        validator("ReviewInput").validate({"action": "correct", "corrected_value": "USB-C"})
        with self.assertRaises(ValidationError):
            validator("ReviewInput").validate({"action": "correct"})
        with self.assertRaises(ValidationError):
            validator("DraftValidationResult").validate({"valid": True, "conflicts": [], "requires_user_review": False})
        self.assertNotIn("/drafts/commit", API["paths"])
        self.assertNotIn("commit_token", SCHEMAS["DraftValidationResult"]["properties"])

    def test_restore_roundtrip_has_usable_tokens_and_typed_confirmation(self):
        reauth = API["paths"]["/auth/reauth"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]
        dryrun = API["paths"]["/backups/{id}/restore-dry-run"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]
        self.assertEqual({"$ref": "#/components/schemas/ReauthResult"}, reauth)
        self.assertEqual({"$ref": "#/components/schemas/RestorePlan"}, dryrun)
        self.assertIn("reauth_token", SCHEMAS["ReauthResult"]["required"])
        self.assertIn("dry_run_id", SCHEMAS["RestorePlan"]["required"])
        with self.assertRaises(ValidationError):
            validator("RestoreConfirm").validate({"reauth_token": "token", "dry_run_id": "plan", "confirm_dataset_reset": True})

    def test_model_manifest_requires_integrity_size_and_compatibility(self):
        self.assertLessEqual({"model_key", "revision", "preprocess_version", "dimension", "normalization", "assets", "min_app_version"},
                             set(SCHEMAS["ModelManifest"]["required"]))
        self.assertLessEqual({"sha256", "size_bytes", "url"}, set(SCHEMAS["ModelAsset"]["required"]))
        with self.assertRaises(ValidationError):
            validator("ModelAsset").validate({"name": "model", "url": "https://untrusted.example/model.onnx",
                "sha256": "a" * 64, "size_bytes": 100, "kind": "model"})

    def test_worker_completions_require_kind_specific_results(self):
        base = {"lease_token": "x" * 32, "source_sha256": "a" * 64, "model_key": "test@1", "kind": "embedding"}
        with self.assertRaises(ValidationError):
            validator("WorkerCompletion").validate(base)
        with self.assertRaises(ValidationError):
            validator("WorkerCompletion").validate({**base, "embedding": []})
        validator("WorkerCompletion").validate({**base, "embedding": [1.0, 0.0]})

    def test_financial_and_inventory_totals_are_dimensioned(self):
        props = SCHEMAS["LocationSummary"]["properties"]
        self.assertNotIn("direct_quantity_base", props)
        self.assertNotIn("subtree_quantity_base", props)
        self.assertIn("direct_quantities", props)
        self.assertIn("subtree_quantities", props)
        self.assertIn("currency_totals", SCHEMAS["AnalyticsOverview"]["properties"])
        self.assertLessEqual({"projected_net_cost_minor", "net_cost_minor", "total_usage", "elapsed_days", "timezone", "day_policy"},
                             set(SCHEMAS["AssetCostSummary"]["required"]))


class OfflineProjectionTests(unittest.TestCase):
    def setUp(self):
        self.check = Draft202012Validator(PROJECTION, format_checker=FORMAT)

    def test_schema_is_valid_and_matches_openapi(self):
        Draft202012Validator.check_schema(PROJECTION)
        embedded = deepcopy(SCHEMAS["OfflineRecord"])
        self.assertNotIn("$defs", embedded)
        embedded["$defs"] = {}
        for entity in PROJECTION["$defs"]:
            component = "Offline" + "".join(part.title() for part in entity.split("_"))
            embedded["$defs"][entity] = deepcopy(SCHEMAS[component])
        text = json.dumps(embedded)
        for entity in PROJECTION["$defs"]:
            component = "Offline" + "".join(part.title() for part in entity.split("_"))
            text = text.replace(f'"#/components/schemas/{component}"', f'"#/$defs/{entity}"')
        standalone = {k: v for k, v in PROJECTION.items() if k not in {"$schema", "$id"}}
        self.assertEqual(standalone, json.loads(text))

    def test_all_22_projection_types_and_tombstones_validate(self):
        self.assertEqual(22, len(PROJECTION["$defs"]))
        for entity in PROJECTION["$defs"]:
            with self.subTest(entity=entity):
                self.check.validate(record(entity))
                self.check.validate(record(entity, operation="delete"))

    def test_unknown_fields_revisions_and_incorrect_tombstones_are_rejected(self):
        cases = [record("item", revision=0), record("item", operation="delete", payload={}),
                 {**record("item"), "projection_version": 2}, {**record("item"), "credentials": "not-allowed"}]
        for payload in cases:
            with self.subTest(payload=payload), self.assertRaises(ValidationError):
                self.check.validate(payload)

    def test_barcode_and_media_link_identity_is_stable_not_compound(self):
        barcode = example(PROJECTION["$defs"]["barcode"])
        barcode.update(id="barcode-1", item_id="item-1", raw_value="0012345678905", normalized_value="0012345678905")
        second = {**barcode, "id": "barcode-2", "item_id": "item-2"}
        self.check.validate(record("barcode", barcode, identity="barcode-1"))
        self.check.validate(record("barcode", second, identity="barcode-2"))
        self.assertEqual(barcode["normalized_value"], second["normalized_value"])
        self.assertNotIn("owner_id", PROJECTION["$defs"]["media"]["properties"])
        self.assertLessEqual({"id", "media_id", "owner_id"}, set(PROJECTION["$defs"]["media_link"]["required"]))

    def test_stock_uses_base_integer_and_never_money_precision(self):
        payload = example(PROJECTION["$defs"]["stock"])
        payload.update(quantity_base=1500, unit_code="kg", base_unit="g", scale_factor=1000)
        self.check.validate(record("stock", payload, identity='["item-1","location-1"]'))
        for bad in [{**payload, "quantity_minor": 1500}, {**payload, "quantity_base": 1.5}, {**payload, "quantity_base": -1}]:
            with self.subTest(bad=bad), self.assertRaises(ValidationError):
                self.check.validate(record("stock", bad))

    def test_unreviewed_or_rejected_ai_cannot_publish_effective_text(self):
        payload = example(PROJECTION["$defs"]["ai_review"])
        for state in ["needs_review", "rejected"]:
            with self.subTest(state=state), self.assertRaises(ValidationError):
                self.check.validate(record("ai_review", {**payload, "state": state, "effective_value": "unapproved"}))
        self.check.validate(record("ai_review", {**payload, "state": "corrected", "effective_value": "USB-C"}))
        for entity in PROJECTION["$defs"].values():
            self.assertTrue({"raw_text", "raw_value", "password_hash", "token_hash"}.isdisjoint(entity["properties"]) or entity is PROJECTION["$defs"]["barcode"])


if __name__ == "__main__":
    unittest.main()
