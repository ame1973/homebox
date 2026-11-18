"""Authenticated connectivity reports reachability without promising health or sync."""
import unittest

from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, SCHEMAS, validator


class ConnectivityContractTests(unittest.TestCase):
    def test_connectivity_is_a_protected_no_store_non_etagged_read(self):
        operation = API["paths"]["/system/connectivity"]["get"]
        self.assertEqual("get_system_connectivity", operation["operationId"])
        self.assertEqual([{"SessionCookie": []}], operation.get("security", API["security"]))
        self.assertEqual("#/components/schemas/ConnectivityStatus", operation["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
        self.assertEqual("no-store", operation["responses"]["200"]["headers"]["Cache-Control"]["schema"]["const"])
        self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        self.assertNotIn("requestBody", operation)
        self.assertNotIn("parameters", operation)

    def test_connectivity_returns_only_a_bounded_status(self):
        schema = SCHEMAS["ConnectivityStatus"]
        self.assertFalse(schema["additionalProperties"])
        self.assertEqual(["status"], schema["required"])
        for status in ("ok", "degraded"):
            validator("ConnectivityStatus").validate({"status": status})
        for value in ({}, {"status": "unknown"}, {"status": None}, {"status": "ok", "secret": "private"}):
            with self.subTest(value=value), self.assertRaises(ValidationError):
                validator("ConnectivityStatus").validate(value)
