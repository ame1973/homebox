"""Validate CI security boundaries without accessing GitHub or the local source pack."""
import re
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


class CIConfigurationTests(unittest.TestCase):
    def test_actions_are_pinned_and_pull_requests_have_no_write_permissions(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        self.assertEqual({"push", "pull_request"}, set(workflow["on"]))
        self.assertEqual({"contents": "read"}, workflow["permissions"])
        for job in workflow["jobs"].values():
            self.assertNotIn("permissions", job)
            for step in job["steps"]:
                if "uses" in step:
                    self.assertRegex(step["uses"], r"^[\w-]+/[\w-]+@[a-f0-9]{40}$")
                if step.get("uses", "").startswith("actions/checkout@"):
                    self.assertIs(False, step["with"]["persist-credentials"])
                if "run" in step:
                    self.assertNotIn("${{", step["run"])
                    self.assertFalse(re.search(r"\bdev/", step["run"]))

    def test_backend_checks_keep_the_full_fresh_race_suite_with_a_bounded_runtime(self):
        makefile = (ROOT / "Makefile").read_text()
        race_target = re.search(r"(?m)^test-go:\n\t([^\n]+)$", makefile)
        self.assertIsNotNone(race_target)
        self.assertEqual(
            "go -C server test -race -count=1 -timeout=20m ./...",
            race_target.group(1),
        )
        self.assertIn("check-backend: test-contracts test-go", makefile)
        self.assertIn("CGO_ENABLED=0 go -C server test -count=1 ./...", makefile)

    def test_frontend_dependencies_are_exact_and_have_no_lifecycle_scripts(self):
        import json
        package = json.loads((ROOT / "web/package.json").read_text())
        lock = json.loads((ROOT / "web/package-lock.json").read_text())
        for kind in ("dependencies", "devDependencies"):
            self.assertEqual(package[kind], lock["packages"][""][kind])
            for version in package[kind].values():
                self.assertRegex(version, r"^\d+\.\d+\.\d+$")
        for hook in ("preinstall", "install", "postinstall", "prepare"):
            self.assertNotIn(hook, package["scripts"])
        self.assertIn("--ignore-scripts", (ROOT / "Makefile").read_text())
