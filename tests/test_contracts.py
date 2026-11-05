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


class UniqueKeySafeLoader(yaml.SafeLoader):
    """Do not silently replace an earlier contract definition with a duplicate key."""

    def construct_mapping(self, node, deep=False):
        keys = set()
        for key_node, _ in node.value:
            key = self.construct_object(key_node, deep=deep)
            if key in keys:
                raise ValueError(f"Duplicate YAML key at line {key_node.start_mark.line + 1}: {key}")
            keys.add(key)
        return super().construct_mapping(node, deep=deep)


ROOT = Path(__file__).resolve().parents[1]
API = yaml.load((ROOT / "contracts/openapi.yaml").read_text(), Loader=UniqueKeySafeLoader)
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
    def test_yaml_rejects_duplicate_keys_and_unsafe_object_tags(self):
        with self.assertRaises(ValueError):
            yaml.load("operation:\n  description: first\n  description: replaced\n", Loader=UniqueKeySafeLoader)
        with self.assertRaises(yaml.constructor.ConstructorError):
            yaml.load("!!python/object:builtins.object {}", Loader=UniqueKeySafeLoader)

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

    def test_auth_transport_cookie_and_body_limits_are_explicit(self):
        for path in ["/auth/login", "/auth/setup", "/auth/logout", "/auth/password", "/auth/reauth"]:
            with self.subTest(path=path):
                operation = API["paths"][path]["post"]
                origins = [parameter for parameter in operation["parameters"] if parameter["name"] == "Origin"]
                self.assertEqual(1, len(origins))
                self.assertTrue(origins[0]["required"])
                self.assertEqual("header", origins[0]["in"])
                self.assertEqual(2048, origins[0]["schema"]["maxLength"])
                if path != "/auth/logout":
                    self.assertEqual(16384, operation["x-max-body-bytes"])
                if path in {"/auth/login", "/auth/logout", "/auth/password"}:
                    self.assertIn("Set-Cookie", operation["responses"]["200"]["headers"])
        self.assertNotIn("requestBody", API["paths"]["/auth/logout"]["post"])
        self.assertNotIn("Set-Cookie", API["paths"]["/auth/setup"]["post"]["responses"]["200"]["headers"])
        self.assertEqual("homebox_session", API["components"]["securitySchemes"]["SessionCookie"]["name"])

    def test_auth_payloads_are_exact_and_credentials_are_not_admin_properties(self):
        for name, value in [
            ("LoginInput", {"username": "owner", "password": "a valid password"}),
            ("AuthSetupInput", {"bootstrap_token": "a" * 43, "username": "owner", "password": "a valid password"}),
            ("AuthPasswordChange", {"old_password": "a valid password", "new_password": "another valid password"}),
        ]:
            validator(name).validate(value)
            for invalid in [{**value, "unexpected": "x"}, {**value, "password_hash": "secret"},
                            {field: content for field, content in value.items() if field != next(iter(value))}]:
                with self.subTest(schema=name, value=invalid), self.assertRaises(ValidationError):
                    validator(name).validate(invalid)
        self.assertEqual({"id", "revision", "username", "must_change_password"}, set(SCHEMAS["Admin"]["properties"]))
        self.assertNotIn("session_token", SCHEMAS["ReauthResult"]["properties"])

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

    def test_location_writes_require_origin_and_document_real_body_limits(self):
        for path, method, limit in [("/locations", "post", 131072),
                                    ("/locations/{id}", "patch", 131072),
                                    ("/locations/{id}", "delete", 0),
                                    ("/locations/{id}/move", "post", 131072),
                                    ("/locations/{id}/archive", "post", 131072)]:
            operation = API["paths"][path][method]
            headers = {p["name"]: p for p in operation["parameters"] if p["in"] == "header"}
            with self.subTest(path=path, method=method):
                self.assertTrue(headers["Origin"]["required"])
                self.assertEqual(limit, operation["x-max-body-bytes"])
                revision = Draft202012Validator(headers["If-Match"]["schema"])
                revision.validate('"1"')
                revision.validate('"9007199254740991"')
                for invalid in ['1', '"0"', '"01"', 'W/"1"', '*', '"1", "2"']:
                    with self.assertRaises(ValidationError):
                        revision.validate(invalid)

    def test_location_integer_schemas_allow_exact_integral_json_numbers(self):
        for number in [1.0, 1000.0, -9007199254740991, 9007199254740991]:
            validator("LocationCreate").validate({"name": "Box", "code": "BOX", "sort_order": number})
        for number in [0.5, -9007199254740992, 9007199254740992]:
            with self.assertRaises(ValidationError):
                validator("LocationCreate").validate({"name": "Box", "code": "BOX", "sort_order": number})

    def test_location_create_parent_proof_defaults_and_safe_bounds(self):
        operation = API["paths"]["/locations"]["post"]
        headers = {p["name"]: p for p in operation["parameters"] if p["in"] == "header"}
        self.assertFalse(headers["If-Match"]["required"])
        self.assertEqual("parent_id", operation["x-parent-revision-required-when"])
        validator("LocationCreate").validate({"name": "Box", "code": "BOX-1"})
        props = SCHEMAS["LocationCreate"]["properties"]
        self.assertEqual("other", props["kind"]["default"])
        self.assertEqual("active", props["status"]["default"])
        self.assertEqual(0, props["sort_order"]["default"])
        for patch in [{"kind": ""}, {"kind": "x" * 33}, {"parent_id": ""},
                      {"parent_id": "x" * 129}, {"sort_order": 9007199254740992},
                      {"sort_order": -9007199254740992}, {"sort_order": 0.5}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("LocationCreate").validate({"name": "Box", "code": "BOX-1", **patch})
        validator("LocationCreate").validate({"name": "📦" * 200, "code": "📦" * 100,
                                               "parent_id": None, "sort_order": -9007199254740991})

    def test_location_patch_preserves_nullable_parent_not_command_wrappers(self):
        for payload in [{"name": "New name"}, {"parent_id": None}, {"parent_id": "parent"},
                        {"description": ""}, {"sort_order": -1}, {"status": "active"}]:
            validator("LocationWrite").validate(payload)
        for payload in [{}, {"parent_id": {"set": True, "value": None}}, {"parent_id": ""},
                        {"kind": None}, {"sort_order": 9007199254740992}, {"unexpected": True}]:
            with self.subTest(payload=payload), self.assertRaises(ValidationError):
                validator("LocationWrite").validate(payload)
        validator("LocationMove").validate({"target_parent_id": None})
        with self.assertRaises(ValidationError):
            validator("LocationMove").validate({"target_parent_id": ""})

    def test_location_response_is_offline_compatible_without_inner_revision(self):
        payload = {"id": "location-1", "parent_id": None, "code": "BOX-1", "name": "Box",
                   "status": "active", "kind": "other", "sort_order": -1, "description": ""}
        validator("Location").validate({**payload, "revision": 1})
        Draft202012Validator(PROJECTION).validate(record("location", payload, identity=payload["id"]))
        for revision in [0, -1, 9007199254740992]:
            with self.subTest(revision=revision), self.assertRaises(ValidationError):
                validator("Location").validate({**payload, "revision": revision})
        for payload_patch in [{"id": ""}, {"parent_id": ""}, {"kind": "x" * 33}]:
            with self.subTest(patch=payload_patch), self.assertRaises(ValidationError):
                validator("Location").validate({**payload, **payload_patch, "revision": 1})

    def test_location_tree_has_bounded_non_snapshot_pagination(self):
        operation = API["paths"]["/locations/tree"]["get"]
        parameters = {p["name"]: p for p in operation["parameters"]}
        self.assertEqual(100, parameters["limit"]["schema"]["maximum"])
        self.assertEqual(1, parameters["limit"]["schema"]["minimum"])
        self.assertEqual(2048, parameters["cursor"]["schema"]["maxLength"])
        validator("LocationList").validate({"data": [], "next_cursor": None})

    def test_item_creation_requires_name_but_defaults_to_individual(self):
        name = API["paths"]["/items"]["post"]["requestBody"]["content"]["application/json"]["schema"]["$ref"].split("/")[-1]
        self.assertEqual("ItemCreate", name)
        validator(name).validate({"name": "USB-C cable"})
        self.assertEqual("individual", SCHEMAS[name]["properties"]["tracking_mode"]["default"])
        for payload in [{}, {"name": ""}, {"name": "Cable", "unexpected": True}]:
            with self.subTest(payload=payload), self.assertRaises(ValidationError):
                validator(name).validate(payload)

    def test_item_create_defaults_and_explicit_nondefault_measurement(self):
        props = SCHEMAS["ItemCreate"]["properties"]
        for field, default in {"tracking_mode": "individual", "status": "active",
                               "unit_code": "pcs", "base_unit": "pcs", "scale_factor": 1,
                               "description": "", "notes": ""}.items():
            with self.subTest(field=field):
                self.assertEqual(default, props[field]["default"])
        for measurement in [{}, {"unit_code": "pcs"}, {"base_unit": "pcs"}, {"scale_factor": 1},
                            {"unit_code": "kg", "base_unit": "g", "scale_factor": 1000},
                            {"unit_code": "L", "base_unit": "ml", "scale_factor": 1000.0},
                            {"unit_code": "custom", "base_unit": "custom/32", "scale_factor": 9007199254740991}]:
            validator("ItemCreate").validate({"name": "Stock", **measurement})
        for measurement in [{"unit_code": "kg"}, {"base_unit": "g"}, {"scale_factor": 1000},
                            {"unit_code": "kg", "base_unit": "g"},
                            {"unit_code": "kg", "scale_factor": 1000},
                            {"base_unit": "g", "scale_factor": 1000}]:
            with self.subTest(measurement=measurement), self.assertRaises(ValidationError):
                validator("ItemCreate").validate({"name": "Stock", **measurement})

    def test_item_nullable_partial_fields_and_status_restore(self):
        for patch in [{"name": "  unchanged whitespace  "}, {"brand": None}, {"brand": ""},
                      {"model": None}, {"model": ""}, {"category_id": None}, {"category_id": "category"},
                      {"description": ""}, {"notes": ""}, {"tracking_mode": "quantity"},
                      {"unit_code": "kg"}, {"base_unit": "g"}, {"scale_factor": 1000},
                      {"status": "active"}, {"status": "archived"}]:
            with self.subTest(patch=patch):
                validator("ItemWrite").validate(patch)
        for patch in [{}, {"name": None}, {"description": None}, {"notes": None},
                      {"tracking_mode": None}, {"unit_code": None}, {"base_unit": None},
                      {"scale_factor": None}, {"status": None},
                      {"brand": {"set": True, "value": None}},
                      {"category_id": {"set": True, "value": None}}, {"quantity_base": 1}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("ItemWrite").validate(patch)

    def test_item_fields_have_independent_safe_bounds_without_changing_generic_aliases(self):
        value = {"id": "📦" * 128, "revision": 9007199254740991, "name": "📦" * 300,
                 "brand": "📦" * 300, "model": "📦" * 300, "category_id": "📦" * 128,
                 "description": "📦" * 10000, "notes": "📦" * 10000, "tracking_mode": "quantity",
                 "unit_code": "📦" * 32, "base_unit": "g" + "_" * 31,
                 "scale_factor": 9007199254740991, "status": "archived"}
        validator("Item").validate(value)
        invalid = [{"name": ""}, {"name": "x" * 301}, {"brand": "x" * 301}, {"model": "x" * 301},
                   {"category_id": ""}, {"category_id": "x" * 129}, {"description": "x" * 10001},
                   {"notes": "x" * 10001}, {"unit_code": ""}, {"unit_code": "x" * 33},
                   {"base_unit": "1g"}, {"base_unit": " g"}, {"base_unit": "g "},
                   {"base_unit": "g" + "\n"}, {"base_unit": "克"}, {"base_unit": "x" * 33},
                   {"scale_factor": 0}, {"scale_factor": -1}, {"scale_factor": 0.5},
                   {"scale_factor": 9007199254740992}, {"tracking_mode": "mixed"}, {"status": "deleted"},
                   {"raw_ocr": "not public"}]
        for schema in ["Item", "ItemCreate", "ItemWrite"]:
            base = value if schema == "Item" else {key: entry for key, entry in value.items()
                                                    if key not in {"id", "revision"}}
            for patch in invalid:
                with self.subTest(schema=schema, patch=patch), self.assertRaises(ValidationError):
                    validator(schema).validate({**base, **patch})
        for patch in [{"id": ""}, {"id": "x" * 129}, {"revision": 0}, {"revision": -1},
                      {"revision": 0.5}, {"revision": 9007199254740992}]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("Item").validate({**value, **patch})
        self.assertEqual({"type": "string"}, SCHEMAS["Unit"]["properties"]["asset_code"])
        self.assertEqual({"type": "integer"}, SCHEMAS["Unit"]["properties"]["revision"])

    def test_item_response_nullable_presence_and_offline_shape(self):
        payload = {"id": "item", "name": "Item", "tracking_mode": "individual", "unit_code": "pcs",
                   "category_id": None, "brand": None, "model": None, "description": "", "notes": "",
                   "base_unit": "pcs", "scale_factor": 1, "status": "active"}
        validator("Item").validate({**payload, "revision": 1})
        Draft202012Validator(PROJECTION).validate(record("item", payload, identity="item"))
        for field in payload:
            with self.subTest(field=field), self.assertRaises(ValidationError):
                validator("Item").validate({**{key: value for key, value in payload.items() if key != field},
                                            "revision": 1})
        self.assertEqual(set(payload), set(SCHEMAS["Item"]["properties"]) - {"revision"})
        for reason in [{}, {"reason": ""}, {"reason": "📦" * 10000}]:
            validator("ItemArchiveInput").validate(reason)
        for reason in [{"reason": None}, {"reason": "x" * 10001}, {"dispose_units": True}]:
            with self.subTest(reason=reason), self.assertRaises(ValidationError):
                validator("ItemArchiveInput").validate(reason)

    def test_item_writes_bind_origin_original_positive_proof_and_bounded_body(self):
        for path, method, limit in [("/items", "post", 131072), ("/items/{id}", "patch", 131072),
                                    ("/items/{id}", "delete", 0), ("/items/{id}/archive", "post", 131072)]:
            operation = API["paths"][path][method]
            headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
            with self.subTest(path=path, method=method):
                self.assertTrue(headers["Origin"]["required"])
                self.assertEqual(2048, headers["Origin"]["schema"]["maxLength"])
                self.assertEqual(limit, operation["x-max-body-bytes"])
                self.assertNotIn("Set-Cookie", operation["responses"]["200"]["headers"])
                if path == "/items":
                    self.assertNotIn("If-Match", headers)
                else:
                    self.assertTrue(headers["If-Match"]["required"])
                    proof = Draft202012Validator(headers["If-Match"]["schema"])
                    for valid in ['"1"', '"9007199254740991"']:
                        proof.validate(valid)
                    for invalid in ['1', '"0"', '"01"', 'W/"1"', '*', '"1", "2"', '"10000000000000000"']:
                        with self.subTest(proof=invalid), self.assertRaises(ValidationError):
                            proof.validate(invalid)
                if method == "delete":
                    self.assertNotIn("requestBody", operation)
                else:
                    self.assertEqual({"application/json"}, set(operation["requestBody"]["content"]))

    def test_item_lists_preserve_all_filters_and_require_complete_bounded_pages(self):
        operation = API["paths"]["/items"]["get"]
        params = {entry["name"]: entry["schema"] for entry in operation["parameters"]}
        self.assertEqual({"from", "to", "currency", "category_id", "location_id", "limit", "cursor",
                          "scope", "status", "merchant", "q"}, set(params))
        self.assertEqual({"type": "integer", "minimum": 1, "maximum": 100, "default": 50}, params["limit"])
        self.assertEqual(["active", "archived"], params["status"]["enum"])
        self.assertEqual(2048, params["cursor"]["maxLength"])
        validator("ItemList").validate({"data": [], "next_cursor": None})
        validator("ItemList").validate({"data": [], "next_cursor": "x" * 2048})
        for page in [{"data": []}, {"data": [], "next_cursor": ""}, {"data": [], "next_cursor": "x" * 2049},
                     {"data": None, "next_cursor": None}]:
            with self.subTest(page=page), self.assertRaises(ValidationError):
                validator("ItemList").validate(page)
        self.assertEqual(100, SCHEMAS["ItemList"]["properties"]["data"]["maxItems"])
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        for path in ["/items", "/items/{id}"]:
            self.assertEqual(0, API["paths"][path]["get"]["x-max-body-bytes"])

    def test_item_date_and_text_filters_preserve_strict_civil_bounds_and_all_query_fields(self):
        operation = API["paths"]["/items"]["get"]
        params = {entry["name"]: entry["schema"] for entry in operation["parameters"]}
        for field in ["from", "to"]:
            check = Draft202012Validator(params[field], format_checker=FORMAT)
            for value in ["0001-01-01", "2024-02-29", "9999-12-31"]:
                check.validate(value)
            for value in ["0000-01-01", "2026-02-29", "2026-2-01", "2026-11-05T00:00:00Z"]:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    check.validate(value)
        for field, good, bad in [("q", ["", "濾", "😀" * 500], ["x" * 501]),
                                 ("merchant", [" Exact merchant ", "😀" * 300], ["", "x" * 301]),
                                 ("currency", ["TWD", "USD"], ["usd", "US", "USDX"])]:
            check = Draft202012Validator(params[field])
            for value in good:
                check.validate(value)
            for value in bad:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    check.validate(value)
        for rule in ["before page slicing", "exact stored merchant", "UTC when unset", "read-only",
                     "home timezone", "never authorize", "not frozen snapshots"]:
            self.assertIn(rule, operation["description"])

    def test_item_resources_return_only_strong_revision_etags(self):
        for path, method in [("/items", "post"), ("/items/{id}", "get"),
                             ("/items/{id}", "patch"), ("/items/{id}/archive", "post")]:
            schema = API["paths"][path][method]["responses"]["200"]["headers"]["ETag"]["schema"]
            check = Draft202012Validator(schema)
            check.validate('"9007199254740991"')
            for value in ['1', 'W/"1"', '"0"', '"01"', '*', '"1", "2"']:
                with self.subTest(path=path, method=method, etag=value), self.assertRaises(ValidationError):
                    check.validate(value)

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


    def test_taxonomy_creation_patch_and_nullable_parent_are_distinct(self):
        validator("CategoryCreate").validate({"name": "  分類 😀  "})
        validator("CategoryCreate").validate({"name": "分類", "parent_id": None})
        validator("CategoryWrite").validate({"parent_id": None})
        validator("CategoryWrite").validate({"parent_id": "parent"})
        for schema, value in [
            ("CategoryCreate", {}), ("CategoryCreate", {"parent_id": "parent"}),
            ("CategoryWrite", {}), ("CategoryWrite", {"name": None}),
            ("CategoryWrite", {"parent_id": {"set": True, "value": None}}),
            ("CategoryWrite", {"parent_id": ""}), ("CategoryWrite", {"parent_id": "x" * 129}),
            ("TagWrite", {}), ("TagWrite", {"name": None}), ("TagWrite", {"name": "", "extra": True}),
        ]:
            with self.subTest(schema=schema, value=value), self.assertRaises(ValidationError):
                validator(schema).validate(value)
        for schema in ["CategoryCreate", "CategoryWrite", "TagWrite"]:
            validator(schema).validate({"name": "😀" * 200})
            for value in ["", "x" * 201]:
                with self.subTest(schema=schema, name=value), self.assertRaises(ValidationError):
                    validator(schema).validate({"name": value})

    def test_taxonomy_resources_have_bounded_identity_positive_revision_and_exact_payloads(self):
        for schema, value in [("Category", {"id": "category", "parent_id": None, "name": "分類", "revision": 1}),
                              ("Tag", {"id": "tag", "name": "標籤", "revision": 1})]:
            validator(schema).validate(value)
            payload = {key: entry for key, entry in value.items() if key != "revision"}
            Draft202012Validator(PROJECTION["$defs"][schema.lower()]).validate(payload)
            for change in [{"id": ""}, {"id": "x" * 129}, {"name": ""}, {"name": "x" * 201},
                           {"revision": 0}, {"revision": -1}, {"revision": 1.5}, {"revision": 9007199254740992}]:
                with self.subTest(schema=schema, change=change), self.assertRaises(ValidationError):
                    validator(schema).validate({**value, **change})
        with self.assertRaises(ValidationError):
            validator("Category").validate({"id": "category", "name": "分類", "revision": 1})

    def test_taxonomy_writes_require_origin_safe_proofs_and_bounded_bodies(self):
        for path in ["/categories", "/tags", "/categories/{id}", "/tags/{id}"]:
            for method, operation in API["paths"][path].items():
                if method == "get":
                    continue
                with self.subTest(path=path, method=method):
                    headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
                    self.assertTrue(headers["Origin"]["required"])
                    self.assertEqual(2048, headers["Origin"]["schema"]["maxLength"])
                    self.assertEqual(0 if method == "delete" else 16384, operation["x-max-body-bytes"])
                    if method != "post" or path == "/categories":
                        proof = headers["If-Match"]
                        self.assertEqual('^"[1-9][0-9]*"$', proof["schema"]["pattern"])
                        self.assertEqual(18, proof["schema"]["maxLength"])
                        self.assertEqual(method != "post", proof["required"])
        creation = API["paths"]["/categories"]["post"]
        self.assertEqual("#/components/schemas/CategoryCreate", creation["requestBody"]["content"]["application/json"]["schema"]["$ref"])
        self.assertEqual("parent_id is non-null", next(entry for entry in creation["parameters"] if entry["name"] == "If-Match")["x-required-when"])

    def test_taxonomy_live_lists_are_bounded_and_never_claim_frozen_sync(self):
        for path, schema in [("/categories", "CategoryList"), ("/tags", "TagList")]:
            operation = API["paths"][path]["get"]
            query = {entry["name"]: entry["schema"] for entry in operation["parameters"]}
            self.assertEqual({"type": "integer", "minimum": 1, "maximum": 100, "default": 100}, query["limit"])
            self.assertEqual(2048, query["cursor"]["maxLength"])
            validator(schema).validate({"data": [], "next_cursor": None})
            self.assertEqual(100, SCHEMAS[schema]["properties"]["data"]["maxItems"])
            self.assertEqual(2048, SCHEMAS[schema]["properties"]["next_cursor"]["maxLength"])


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
