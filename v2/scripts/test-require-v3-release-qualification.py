#!/usr/bin/env python3
"""Fixture tests for the exact release qualification gate."""

import importlib.util
import pathlib
import unittest

path = pathlib.Path(__file__).with_name("require-v3-release-qualification.py")
spec = importlib.util.spec_from_file_location("qualification", path)
qualification = importlib.util.module_from_spec(spec)
spec.loader.exec_module(qualification)

SHA = "a" * 40
RUN_ID = "12345"
REPO = "antiartificial/norn"
BASE = "/repos/" + REPO + "/actions"


def fixture():
    jobs = [dict(id=index + 1, run_id=int(RUN_ID), run_attempt=2, name=name,
                 status="completed", conclusion="success")
            for index, name in enumerate(sorted(qualification.EXPECTED_JOBS))]
    return {
        BASE + "/workflows/v3-ci.yml": dict(id=89, path=qualification.WORKFLOW_PATH, state="active"),
        BASE + "/runs/" + RUN_ID: dict(id=int(RUN_ID), workflow_id=89, event="workflow_dispatch",
            head_branch="master", head_sha=SHA, status="completed", conclusion="success",
            path=qualification.WORKFLOW_PATH + "@master", repository=dict(full_name=REPO),
            run_attempt=2),
        BASE + "/runs/" + RUN_ID + "/attempts/2/jobs?per_page=100&page=1":
            dict(total_count=len(jobs), jobs=jobs),
    }


class QualificationTests(unittest.TestCase):
    def test_workflow_rechecks_before_signing(self):
        workflow = (path.parents[2] / ".github/workflows/platform-release.yml").read_text()
        authorize, publish = workflow.split("  publish:\n", 1)
        self.assertIn("Require exact protected v3 CI qualification", authorize)
        self.assertIn("actions: read", publish.split("    steps:\n", 1)[0])
        recheck = publish.index("Recheck exact protected v3 CI qualification before signing")
        sign = publish.index("Sign, verify, and publish without overwriting")
        self.assertLess(recheck, sign)
        gate_step = publish[recheck:sign]
        self.assertIn("run: python3 v2/scripts/require-v3-release-qualification.py", gate_step)
        self.assertNotIn("NORN_RELEASE_SIGNING_KEY", gate_step)

    def check(self, responses):
        def get(path, token):
            self.assertEqual(token, "token")
            return responses[path]
        return qualification.require_qualification(REPO, SHA, RUN_ID, "token", get)

    def test_complete_rerun_attempt(self):
        self.assertEqual(self.check(fixture()), (2, len(qualification.EXPECTED_JOBS)))

    def test_run_identity_and_result_rejections(self):
        for field, bad in (("head_sha", "b" * 40), ("event", "push"),
                           ("head_branch", "feature"), ("status", "in_progress"),
                           ("conclusion", "failure"), ("workflow_id", 90),
                           ("path", ".github/workflows/other.yml@master")):
            with self.subTest(field=field):
                responses = fixture()
                responses[BASE + "/runs/" + RUN_ID][field] = bad
                with self.assertRaises(ValueError):
                    self.check(responses)

    def test_job_set_and_result_rejections(self):
        key = BASE + "/runs/" + RUN_ID + "/attempts/2/jobs?per_page=100&page=1"
        for bad in ("skipped", "failure", None):
            with self.subTest(conclusion=bad):
                responses = fixture()
                responses[key]["jobs"][0]["conclusion"] = bad
                with self.assertRaises(ValueError):
                    self.check(responses)
        responses = fixture()
        responses[key]["jobs"].pop()
        responses[key]["total_count"] -= 1
        with self.assertRaises(ValueError):
            self.check(responses)
        responses = fixture()
        responses[key]["jobs"][0]["name"] = "unexpected"
        with self.assertRaises(ValueError):
            self.check(responses)
        responses = fixture()
        responses[key]["jobs"][0]["run_attempt"] = 1
        with self.assertRaises(ValueError):
            self.check(responses)

    def test_pagination_missing_page_fails_closed(self):
        responses = fixture()
        key = BASE + "/runs/" + RUN_ID + "/attempts/2/jobs?per_page=100&page=1"
        responses[key]["total_count"] = 101
        with self.assertRaises(ValueError):
            self.check(responses)

    def test_invalid_run_id(self):
        with self.assertRaises(ValueError):
            qualification.require_qualification(REPO, SHA, "123/attempts/1", "token", lambda *_: None)

    def test_rerun_started_during_verification(self):
        responses = fixture()
        reads = 0

        def get(path, token):
            nonlocal reads
            if path == BASE + "/runs/" + RUN_ID:
                reads += 1
                if reads == 2:
                    changed = dict(responses[path])
                    changed["run_attempt"] = 3
                    changed["status"] = "in_progress"
                    return changed
            return responses[path]

        with self.assertRaisesRegex(ValueError, "changed during verification"):
            qualification.require_qualification(REPO, SHA, RUN_ID, "token", get)


if __name__ == "__main__":
    unittest.main()
