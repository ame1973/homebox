"""Verify the server's embedded offline schema and representative payloads."""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[1]
CANONICAL = ROOT / "contracts/offline-projection.schema.json"
EMBEDDED = ROOT / "server/internal/offlineprojection/offline-projection.schema.json"
FIXTURES = ROOT / "server/internal/offlineprojection/testdata/payloads.json"


class OfflineProjectionRuntimeContractTests(unittest.TestCase):
    def test_embedded_schema_is_byte_identical_to_root_contract(self):
        self.assertEqual(CANONICAL.read_bytes(), EMBEDDED.read_bytes())

    def test_all_22_runtime_examples_validate_against_the_root_schema(self):
        schema = json.loads(CANONICAL.read_text())
        fixtures = json.loads(FIXTURES.read_text())
        self.assertEqual(22, len(fixtures))
        self.assertEqual(set(schema["$defs"]), set(fixtures))
        for kind, payload in fixtures.items():
            with self.subTest(kind=kind):
                Draft202012Validator(schema["$defs"][kind], format_checker=FormatChecker()).validate(payload)

    def test_version_one_payload_properties_remain_flat_scalars(self):
        schema = json.loads(CANONICAL.read_text())
        for kind, definition in schema["$defs"].items():
            for name, field in definition["properties"].items():
                with self.subTest(kind=kind, name=name):
                    self.assertFalse(set(field) - {"type", "enum", "minLength", "maxLength", "minimum", "maximum", "pattern", "format", "anyOf"})
                    options = field.get("anyOf", [field])
                    for option in options:
                        self.assertFalse(set(option) - {"type", "enum", "minLength", "maxLength", "minimum", "maximum", "pattern", "format"})
                        types = option.get("type", [])
                        types = [types] if isinstance(types, str) else types
                        self.assertTrue(types or option.get("enum"))
                        self.assertTrue(set(types) <= {"string", "integer", "boolean", "null"})
                        self.assertTrue(all(isinstance(value, str) for value in option.get("enum", [])))
                        if "pattern" in option:
                            self.assertIn(option["pattern"], {"^[a-f0-9]{64}$", "^[A-Z]{3}$"})
                        if "format" in option:
                            self.assertEqual("date-time", option["format"])

    def test_go_validator_matches_independent_schema_mutations(self):
        schema = json.loads(CANONICAL.read_text())
        fixtures = json.loads(FIXTURES.read_text())
        validators = {kind: Draft202012Validator(definition, format_checker=FormatChecker())
                      for kind, definition in schema["$defs"].items()}
        substitutions = [None, True, False, 0, -1, 1, 1.0, 1.5, 9007199254740991, 9007199254740992,
                         "", "x", "ABC", "a" * 64, "界" * 10001,
                         "2026-11-18T00:00:00Z", "2026-11-18t00:00:00z", [], {"nested": 1}]
        cases = []

        def append(kind, payload):
            safe_numbers = all(not isinstance(value, (int, float)) or isinstance(value, bool) or
                               (abs(value) <= 9007199254740991 and value == int(value))
                               for value in payload.values())
            cases.append({"kind": kind, "payload": payload,
                          "valid": safe_numbers and validators[kind].is_valid(payload)})

        for kind, original in fixtures.items():
            append(kind, original)
            for key in original:
                append(kind, {name: value for name, value in original.items() if name != key})
                for replacement in substitutions:
                    append(kind, {**original, key: replacement})
            append(kind, {**original, "private_extra": "not-approved"})

        # The RFC 3339 format checker accepts a trailing LF because of its regex
        # anchor; the runtime intentionally rejects that non-RFC3339 spelling.
        trailing_lf = {**fixtures["cost"], "occurred_at": "2026-11-18T00:00:00Z\n"}
        self.assertTrue(validators["cost"].is_valid(trailing_lf))
        cases.append({"kind": "cost", "payload": trailing_lf, "valid": False})
        self.assertGreaterEqual(len(cases), 1000)
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", suffix=".json") as fixture:
            json.dump(cases, fixture, ensure_ascii=False, separators=(",", ":"))
            fixture.flush()
            env = {**os.environ, "HOMEBOX_PROJECTION_CASES": fixture.name}
            result = subprocess.run(
                ["go", "-C", str(ROOT / "server"), "test", "./internal/offlineprojection",
                 "-run", "^TestProjectionDifferentialFixture$", "-count=1"],
                env=env, capture_output=True, text=True, timeout=120, check=False,
            )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
