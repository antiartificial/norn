from __future__ import annotations

from pathlib import Path
import runpy
import unittest


module = runpy.run_path(str(Path(__file__).with_name("platform-release-qualification")))
qualify = module["validate"]
EXPECTED_JOBS = module["EXPECTED_JOBS"]

SHA = "a" * 40
REPOSITORY = "antiartificial/norn"
RUN_ID = "123456789"


def completed_run() -> dict:
    return {
        "id": int(RUN_ID),
        "run_attempt": 1,
        "event": "workflow_dispatch",
        "path": ".github/workflows/v3-ci.yml",
        "head_branch": "master",
        "head_sha": SHA,
        "status": "completed",
        "conclusion": "success",
        "repository": {"full_name": REPOSITORY},
        "head_repository": {"full_name": REPOSITORY},
    }


def completed_jobs() -> dict:
    return {"total_count": len(EXPECTED_JOBS), "jobs": [
        {"name": name, "status": "completed", "conclusion": "success"}
        for name in sorted(EXPECTED_JOBS)
    ]}


class PlatformReleaseQualificationTests(unittest.TestCase):
    def test_exact_manual_full_ci_run_is_accepted(self) -> None:
        qualify(completed_run(), completed_jobs(), SHA, RUN_ID, REPOSITORY)

    def test_pull_request_m4_run_cannot_qualify_a_release(self) -> None:
        run = completed_run()
        run["event"] = "pull_request"
        with self.assertRaisesRegex(ValueError, "exact successful"):
            qualify(run, completed_jobs(), SHA, RUN_ID, REPOSITORY)

    def test_success_for_another_commit_cannot_qualify(self) -> None:
        run = completed_run()
        run["head_sha"] = "b" * 40
        with self.assertRaisesRegex(ValueError, "exact successful"):
            qualify(run, completed_jobs(), SHA, RUN_ID, REPOSITORY)

    def test_rerun_or_other_repository_cannot_qualify(self) -> None:
        for mutation in ({"run_attempt": 2}, {"head_repository": {"full_name": "someone/fork"}}):
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                qualify(completed_run() | mutation, completed_jobs(), SHA, RUN_ID, REPOSITORY)

    def test_failed_or_unfinished_run_cannot_qualify(self) -> None:
        for mutation in ({"conclusion": "failure"}, {"status": "in_progress"}):
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                qualify(completed_run() | mutation, completed_jobs(), SHA, RUN_ID, REPOSITORY)

    def test_skipped_or_missing_protected_job_cannot_qualify(self) -> None:
        for kind in ("skipped", "missing", "duplicate"):
            jobs = completed_jobs()
            if kind == "skipped":
                jobs["jobs"][0]["conclusion"] = "skipped"
            elif kind == "missing":
                jobs["jobs"].pop()
                jobs["total_count"] -= 1
            else:
                jobs["jobs"][0]["name"] = jobs["jobs"][1]["name"]
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, "qualification"):
                qualify(completed_run(), jobs, SHA, RUN_ID, REPOSITORY)


if __name__ == "__main__":
    unittest.main()
