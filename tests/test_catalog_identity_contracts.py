"""Exact Item identity wire rules; runtime authority is tested separately."""
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, SCHEMAS


class ItemIdentityContractTests(unittest.TestCase):
    def test_exact_identity_uses_the_same_item_and_revision_as_the_path_read(self):
        self.assertEqual({"get"}, set(API["paths"]["/items-by-identity"]))
        operation = API["paths"]["/items-by-identity"]["get"]
        original = API["paths"]["/items/{id}"]["get"]
        self.assertEqual("get_item_by_identity", operation["operationId"])
        self.assertEqual(0, operation["x-max-body-bytes"])
        self.assertEqual(16384, operation["x-max-query-bytes"])
        self.assertEqual("item", operation["x-response-revision-scope"])
        self.assertNotEqual([], operation.get("security", API["security"]))
        self.assertNotIn("requestBody", operation)
        self.assertEqual(original["responses"], operation["responses"])
        self.assertEqual([("id", "query", True)], [(p["name"], p["in"], p["required"]) for p in operation["parameters"]])
        schema = operation["parameters"][0]["schema"]
        self.assertEqual(SCHEMAS["Item"]["properties"]["id"], schema)
        identity = Draft202012Validator(schema)
        for value in ["legacy/item/\x00😀", "tree", ".", "..", "😀" * 128]:
            identity.validate(value)
        for value in ["", "😀" * 129, None, 1]:
            with self.subTest(identity=value), self.assertRaises(ValidationError):
                identity.validate(value)
        for word in ["exact", "path", "completed session", "SQL", "not a collection", "write"]:
            self.assertIn(word, operation["description"])
