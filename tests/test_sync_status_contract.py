"""Sync status describes partial materialization, not a ready offline replica."""
import unittest

from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, PROJECTION, SCHEMAS, validator


MAXIMUM = 9007199254740991
STATUS = {
    "dataset_epoch": "epoch-1", "server_instance_id": "a" * 32, "projection_version": 1,
    "min_retained_seq": 2, "high_watermark": 3, "counts": {"location": 1},
    "counts_scope": "materialized_projections", "last_change_at": "2026-01-01T00:00:00Z",
    "retention_days": 30, "bootstrap_ready": False, "retention_enabled": False,
}


class SyncStatusContractTests(unittest.TestCase):
    def test_status_explains_partial_counts_and_unavailable_bootstrap(self):
        schema = SCHEMAS["SyncStatus"]
        self.assertEqual(set(STATUS), set(schema["required"]))
        self.assertEqual(set(PROJECTION["$defs"]), set(schema["properties"]["counts"]["propertyNames"]["enum"]))
        validator("SyncStatus").validate(STATUS)
        validator("SyncStatus").validate({**STATUS, "last_change_at": None, "counts": {}, "high_watermark": MAXIMUM})
        validator("SyncStatus").validate({**STATUS, "bootstrap_ready": True, "retention_enabled": True})
        for field in STATUS:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("SyncStatus").validate({key: value for key, value in STATUS.items() if key != field})

    def test_status_rejects_unsafe_or_unknown_values(self):
        for patch in [
            {"dataset_epoch": ""}, {"dataset_epoch": "x" * 129},
            {"server_instance_id": "A" * 32}, {"server_instance_id": "a" * 32 + "\n"},
            {"projection_version": 2}, {"min_retained_seq": -1}, {"high_watermark": MAXIMUM + 1},
            {"counts": {"private": 1}}, {"counts": {"__proto__": 1}}, {"counts": {"item": 0}}, {"counts": {"item": MAXIMUM + 1}},
            {"counts_scope": "all_sources"}, {"last_change_at": "yesterday"}, {"retention_days": 0},
            {"bootstrap_ready": 1}, {"retention_enabled": "false"}, {"private": "value"}, {"__proto__": "value"},
        ]:
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("SyncStatus").validate({**STATUS, **patch})

    def test_status_is_a_non_etagged_protected_read(self):
        operation = API["paths"]["/sync/status"]["get"]
        self.assertEqual("get_sync_status", operation["operationId"])
        self.assertEqual("#/components/schemas/SyncStatus", operation["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        self.assertEqual("no-store", operation["responses"]["200"]["headers"]["Cache-Control"]["schema"]["const"])
