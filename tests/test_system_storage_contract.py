"""Database filesystem observations must not turn unknown scopes into zero."""
import unittest

from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, SCHEMAS, validator


MAXIMUM = 9007199254740991
STORAGE = {
    "database_bytes": 2048,
    "media_bytes": None,
    "free_bytes": 8192,
    "backup_bytes": None,
    "database_filesystem_total_bytes": 16384,
    "measured_at": "2026-11-27T12:00:00Z",
}


class SystemStorageContractTests(unittest.TestCase):
    def test_storage_is_a_bodyless_non_etagged_protected_read(self):
        operation = API["paths"]["/system/storage"]["get"]
        self.assertEqual("get_system_storage", operation["operationId"])
        self.assertEqual([{"SessionCookie": []}], operation.get("security", API["security"]))
        self.assertEqual(0, operation["x-max-body-bytes"])
        self.assertNotIn("requestBody", operation)
        self.assertNotIn("parameters", operation)
        self.assertEqual("#/components/schemas/StorageStatus", operation["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
        self.assertEqual("no-store", operation["responses"]["200"]["headers"]["Cache-Control"]["schema"]["const"])
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])

    def test_unknown_and_known_scopes_are_explicit(self):
        schema = SCHEMAS["StorageStatus"]
        self.assertFalse(schema["additionalProperties"])
        self.assertEqual(set(STORAGE), set(schema["required"]))
        validator("StorageStatus").validate(STORAGE)
        validator("StorageStatus").validate({**STORAGE, "media_bytes": 0, "backup_bytes": MAXIMUM})
        validator("StorageStatus").validate({**STORAGE, "measured_at": "2026-11-27T12:00:00.123456789+08:00"})
        for field in STORAGE:
            with self.subTest(missing=field), self.assertRaises(ValidationError):
                validator("StorageStatus").validate({key: value for key, value in STORAGE.items() if key != field})

    def test_counts_are_safe_integers_and_other_fields_are_rejected(self):
        for field in ("database_bytes", "media_bytes", "free_bytes", "backup_bytes", "database_filesystem_total_bytes"):
            for invalid in (-1, MAXIMUM + 1, 1.5, True, "1"):
                with self.subTest(field=field, invalid=invalid), self.assertRaises(ValidationError):
                    validator("StorageStatus").validate({**STORAGE, field: invalid})
            validator("StorageStatus").validate({**STORAGE, field: 0})
            validator("StorageStatus").validate({**STORAGE, field: MAXIMUM})
        for field in ("database_bytes", "free_bytes", "database_filesystem_total_bytes"):
            with self.subTest(field=field, invalid=None), self.assertRaises(ValidationError):
                validator("StorageStatus").validate({**STORAGE, field: None})
        for patch in ({"measured_at": "not-a-time"}, {"measured_at": None},
                      {"measured_at": "2026-11-27T12:00:00.1234567890Z"}, {"path": "/private"}):
            with self.subTest(patch=patch), self.assertRaises(ValidationError):
                validator("StorageStatus").validate({**STORAGE, **patch})
