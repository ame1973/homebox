"""Version observations do not assert unknown compatibility or model indexes."""
import unittest

from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, SCHEMAS, validator


VERSION = {
    "api_version": "1", "projection_version": 1, "offline_projection_version": 1,
    "dataset_epoch": "epoch-1", "server_instance_id": "a" * 32,
    "web_build_id": "b" * 24, "web_version": "0.1.0-dev", "schema_version": 28,
    "min_supported_client": None, "min_web_build_id": None, "model_index_versions": None,
    "supported_write_client_versions": ["0.1.0", "0.1.0-dev"],
}


class SystemVersionContractTests(unittest.TestCase):
    def test_unknown_minimums_and_model_inventory_are_explicit(self):
        schema = SCHEMAS["Version"]
        self.assertEqual(set(VERSION), set(schema["required"]))
        validator("Version").validate(VERSION)
        validator("Version").validate({**VERSION, "model_index_versions": []})
        validator("Version").validate({**VERSION, "model_index_versions": ["pinned-model-1"]})
        for field in VERSION:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("Version").validate({key: value for key, value in VERSION.items() if key != field})

    def test_version_rejects_unknown_fields_and_invalid_exact_write_policy(self):
        for patch in [
            {"server_instance_id": "A" * 32}, {"server_instance_id": "a" * 32 + "\n"},
            {"web_build_id": "A" * 24}, {"web_build_id": "b" * 24 + "\n"},
            {"projection_version": 2}, {"offline_projection_version": 2}, {"schema_version": 0},
            {"supported_write_client_versions": []}, {"supported_write_client_versions": [""]},
            {"supported_write_client_versions": ["1", "1"]}, {"supported_write_client_versions": ["x" * 33]},
            {"model_index_versions": [""]}, {"model_index_versions": ["x" * 257]},
            {"model_index_versions": ["x"] * 501}, {"unexpected": "private"},
        ]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("Version").validate({**VERSION, **patch})

    def test_handshake_is_a_bodyless_non_etagged_protected_read(self):
        operation = API["paths"]["/system/version"]["get"]
        self.assertEqual("get_system_version", operation["operationId"])
        self.assertEqual(0, operation["x-max-body-bytes"])
        self.assertEqual("#/components/schemas/Version", operation["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        self.assertEqual("no-store", operation["responses"]["200"]["headers"]["Cache-Control"]["schema"]["const"])
