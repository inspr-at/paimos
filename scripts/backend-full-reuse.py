#!/usr/bin/env python3
"""Reuse actual hosted backend execution only for frontend/metadata-only main pushes."""
import json
import os
import re
import subprocess


def command(*args):
    return subprocess.check_output(args, stderr=subprocess.DEVNULL).decode()


def frontend_only(path):
    return path.startswith(("frontend/src/", "frontend/public/")) or path in {
        "README.md", "VERSION", "docs/CHANGELOG.md", "docs/INSTALL.md",
    }


def select(head, event, ref, repository, gh="gh"):
    full = {"run_full": "true"}
    if event != "push" or ref != "refs/heads/main":
        return full
    if not re.fullmatch(r"[0-9a-f]{40}", head):
        return full
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        return full
    if command("git", "rev-parse", "HEAD").strip() != head:
        return full
    runs = json.loads(command(
        gh, "run", "list", "--repo", repository, "--workflow", "backend-full.yml",
        "--branch", "main", "--status", "success", "--limit", "50", "--json",
        "databaseId,headSha,event,headBranch,status,conclusion",
    ))
    if not isinstance(runs, list):
        return full
    required = {
        "backend-full-authorize", "backend-full-serial", "backend-full",
        "backend-full-race (core)", "backend-full-race (handlers)",
        "backend-full-race (runtime)",
    }
    for run in runs:
        base, run_id = run.get("headSha", ""), run.get("databaseId")
        if (not re.fullmatch(r"[0-9a-f]{40}", base) or base == head
                or type(run_id) is not int or run_id <= 0
                or run.get("headBranch") != "main"
                or run.get("event") not in {"push", "schedule", "workflow_dispatch"}
                or run.get("status") != "completed" or run.get("conclusion") != "success"):
            continue
        if subprocess.run(["git", "merge-base", "--is-ancestor", base, head],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
            continue
        paths = command("git", "diff", "--no-renames", "--name-only", "-z", base, head).split("\0")
        if any(not frontend_only(path) for path in paths if path):
            continue
        jobs = json.loads(command(gh, "run", "view", str(run_id), "--repo", repository, "--json", "jobs"))["jobs"]
        # A previous reuse-only aggregator is insufficient: require real serial
        # and all three race executions, avoiding chains of skipped suites.
        green = {job["name"] for job in jobs
                 if job.get("status") == "completed" and job.get("conclusion") == "success"}
        if required <= green:
            return {"run_full": "false", "source_sha": base,
                    "source_run": str(run_id),
                    "source_url": f"https://github.com/{repository}/actions/runs/{run_id}"}
    return full


def main():
    try:
        result = select(os.environ.get("GITHUB_SHA", ""), os.environ.get("GITHUB_EVENT_NAME", ""),
                        os.environ.get("GITHUB_REF", ""), os.environ.get("GITHUB_REPOSITORY", ""))
    except (subprocess.SubprocessError, OSError, ValueError, KeyError, TypeError, AttributeError):
        # Unavailable or malformed evidence adds testing; it never skips it.
        result = {"run_full": "true"}
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        for key, value in result.items():
            output.write(f"{key}={value}\n")
    print(json.dumps(result))
    if result["run_full"] == "false":
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write("Backend inputs unchanged; reusing actual full serial and race execution: "
                          f"[{result['source_sha']}]({result['source_url']}). "
                          "Only frontend source/public assets or release metadata changed.\n")


if __name__ == "__main__":
    main()
