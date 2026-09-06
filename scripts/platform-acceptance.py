#!/usr/bin/env python3
"""Prepare pinned Linux/Codex platform proofs; execution requires a reviewed plan.

No credentials are read or copied. The Linux proof runs in the existing Colima
VM and uses a runtime-only unique unit. Codex inherits its normal CLI auth and
may write normal per-session state; this is not shared-home isolation.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import uuid

REPO = Path(__file__).resolve().parents[1]
TESTS = {
    "agentd": "TestCodex(ProcessOwnsExactAppServerSessionForControl|DispatchProfileUsesDocumentedModelAndEffortFields|StartCancellationReapsInFlightAppServer|DrainsFinalCompletionBeforeReapingAppServer|PrematureStreamEOFWaitsForExactChildReap|OnlyOwnedToolItemsReportToolActivity|ForeignCompletionCannotMakeOwnedTurnIdle|EarlyCompletionRequiresReturnedOwnedTurn|EarlyToolActivityRequiresReturnedOwnedTurn|AmbiguousInboxStartClosesInsteadOfReopeningInbox|PersistentTerminalFailureClosesExactOwnedGroup|FailureReachesSupervisorTerminalReporterSnapshot)",
    "runtimehealth": "TestRuntime(PlatformManagersVerifyDeclarativeOwnership|PlatformRecoveryScenarios|ServiceRejectsAmbiguousOrForeignDefinitions|LinuxDropInCannotWidenOwnership|ServiceDefinitionChangesFailClosed|ReporterURLMustMatchNamedDeployment)",
    "ownedprocess": "TestConfigureVerifyAndSignalExactSpawnedGroup",
}


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def run(argv, *, env=None, timeout=180, cwd=None):
    result = subprocess.run(argv, cwd=cwd, env=env, timeout=timeout, capture_output=True)
    if result.returncode:
        # Preserve only bounded build/test diagnostics in the private task log.
        raise RuntimeError(f"command failed ({Path(argv[0]).name}, exit {result.returncode})")
    return result.stdout.decode().strip()


def write(path, value):
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")


def prepare(args):
    root = Path(args.root).resolve()
    if not root.name.startswith("pai928-"):
        raise ValueError("task root basename must start pai928-")
    root.mkdir(mode=0o700, parents=False, exist_ok=False)
    (root / "bin").mkdir(mode=0o700)
    go = str(Path(args.go).resolve())
    go_version = run([go, "version"])
    if "go1.26.6 " not in go_version:
        raise ValueError("Go 1.26.6 is required")
    source = run(["git", "rev-parse", "HEAD"], cwd=REPO)
    tag = "pai928-" + uuid.uuid4().hex[:10]
    vmroot = "/tmp/" + tag
    colima = str(Path(args.colima).resolve())
    if run([colima, "ssh", "--", "uname", "-s", "-m"]) != "Linux aarch64":
        raise ValueError("expected native Linux aarch64 VM")
    if run([colima, "ssh", "--", "systemctl", "--user", "is-system-running"]) != "running":
        raise ValueError("existing user manager is not running")
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64", GOTOOLCHAIN="local")
    helper_sources = [str(REPO / "scripts/platform-proof" / p) for p in ("main.go", "codex.go")]
    run([go, "build", "-trimpath", "-mod=readonly", "-o", str(root / "bin/paimos-agentd"), "./cmd/paimos-agentd"], env=env, cwd=REPO / "backend")
    run([go, "build", "-trimpath", "-mod=readonly", "-o", str(root / "bin/platform-proof-linux"), *helper_sources], env=env, cwd=REPO / "backend")
    native_env = dict(os.environ, CGO_ENABLED="0", GOTOOLCHAIN="local")
    native_env.pop("GOOS", None); native_env.pop("GOARCH", None)
    run([go, "build", "-trimpath", "-mod=readonly", "-o", str(root / "bin/platform-proof-native"), *helper_sources], env=native_env, cwd=REPO / "backend")
    for package in TESTS:
        run([go, "test", "-c", "-mod=readonly", "-o", str(root / "bin" / (package + ".test")), "./" + package], env=env, cwd=REPO / "backend")
    unit = root / ("paimos-agentd-" + tag + ".service")
    # Neither vendor can launch, and there is no reporter, account, or server.
    unit.write_text("[Unit]\nDescription=PAI-928 isolated empty-daemon proof\n[Service]\nType=simple\nExecStart=" + str(root / "bin/paimos-agentd") + " serve --instance " + tag + " --state-root " + vmroot + " --codex-path /usr/bin/false --claude-path /usr/bin/false\nRestart=no\nUMask=0077\nStandardOutput=null\nStandardError=null\n")
    unit.chmod(0o600)
    codex = str(Path(args.codex).resolve())
    auth_status = run([codex, "login", "status"], timeout=10)
    # Some CLI builds use stderr; only the public login-status text is queried.
    if not auth_status:
        probe = subprocess.run([codex, "login", "status"], capture_output=True, timeout=10)
        auth_status = probe.stderr.decode().strip()
    if auth_status != "Logged in using ChatGPT":
        raise ValueError("reviewed ChatGPT auth status not present")
    manifest = {
        "schema_version": 1, "source": source, "go": go_version,
        "root": str(root), "colima": colima, "instance": tag,
        "vm_state_root": vmroot, "unit": str(unit), "unit_sha256": sha(unit),
        "files": {str(p): sha(p) for p in sorted((root / "bin").iterdir())},
        "helper_sources": {p: sha(p) for p in helper_sources},
        "runner_sha256": sha(__file__), "codex": codex, "codex_sha256": sha(codex),
        "codex_version": run([codex, "--version"]), "codex_account_label": "chatgpt",
        "codex_budget": {"queries": 1, "input_submissions": 3, "profile": "codex-sol-high@1", "seconds": 100},
        "cleanup": ["production PlatformService.Stop of exact task unit", "systemctl --user disable --runtime " + unit.name, "systemctl --user daemon-reload", "verify exact unit unloaded and its socket unreachable; retain all task state/evidence"],
        "limits": ["existing local VM, not clean physical-machine proof", "Linux service proof has zero workers and no reporter", "fake-vendor process tests exercise native Linux process ownership", "Codex primitive proof has no server/reporter; normal CLI auth inheritance can create normal per-session state", "no existing Claude instance or shared configuration changes"],
        "execution_approved": False,
    }
    write(root / "plan.json", manifest)
    print(str(root / "plan.json"))


def load_plan(path):
    plan = json.loads(Path(path).read_text())
    root = Path(plan["root"])
    if not root.is_absolute() or not root.name.startswith("pai928-") or not re.fullmatch(r"pai928-[a-f0-9]{10}", plan["instance"]):
        raise ValueError("unsafe plan scope")
    if plan["vm_state_root"] != "/tmp/" + plan["instance"] or Path(plan["unit"]).parent != root:
        raise ValueError("scope mismatch")
    for path, digest in {**plan["files"], **plan["helper_sources"], plan["unit"]: plan["unit_sha256"], plan["codex"]: plan["codex_sha256"], __file__: plan["runner_sha256"]}.items():
        if sha(path) != digest:
            raise ValueError("reviewed pin changed")
    return plan


def execute(args):
    p = load_plan(args.plan); root = Path(p["root"])
    if args.mode == "fixtures":
        rows = []
        for package, pattern in TESTS.items():
            result = subprocess.run([p["colima"], "ssh", "--", str(root / "bin" / (package + ".test")), "-test.run=^(" + pattern + ")$", "-test.count=1", "-test.timeout=45s", "-test.v"], capture_output=True, timeout=60)
            log = root / (package + "-linux.log")
            fd = os.open(log, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(fd,"wb") as f: f.write(result.stdout + result.stderr)
            # A zero-match test invocation must never be reported as proof.
            passed = result.returncode == 0 and b"--- PASS: " in result.stdout and b"no tests to run" not in result.stdout + result.stderr
            rows.append({"package": package, "status": "PASS" if passed else "FAIL", "pattern": pattern, "log": str(log), "sha256": sha(log)})
        write(root / "linux-fixtures.json", {"status": "PASS" if all(r["status"] == "PASS" for r in rows) else "FAIL", "fixture": True, "platform": "linux/arm64", "source": p["source"], "suites": rows})
        if not all(r["status"] == "PASS" for r in rows): raise RuntimeError("Linux fixture failure; retained logs")
    else:
        if not args.execute_reviewed_plan:
            raise ValueError("service/vendor execution requires explicit reviewed-plan flag")
        mode = args.mode
        binary = str(root / "bin/paimos-agentd") if mode == "service" else p["codex"]
        helper = str(root / "bin" / ("platform-proof-linux" if mode == "service" else "platform-proof-native"))
        argv = [helper, mode, "--execute-reviewed-plan", "--receipt", str(root / (mode + "-receipt.json")), "--root", p["vm_state_root"] if mode == "service" else str(root / "pai928-codex"), "--instance", p["instance"], "--binary", binary, "--binary-sha256", sha(binary)]
        if mode == "service": argv = [p["colima"], "ssh", "--", *argv, "--unit", p["unit"], "--unit-sha256", p["unit_sha256"]]
        result = subprocess.run(argv, timeout=140, capture_output=True)
        # Runner output is closed-form stage/status only, never vendor payloads.
        print(result.stdout.decode().strip())
        if result.returncode: raise RuntimeError("proof failed; inspect private stage receipt")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    prep = sub.add_parser("prepare")
    for name in ("root", "go", "colima", "codex"): prep.add_argument("--" + name, required=True)
    check = sub.add_parser("run")
    check.add_argument("--plan", required=True)
    check.add_argument("--mode", choices=("fixtures", "service", "codex"), required=True)
    check.add_argument("--execute-reviewed-plan", action="store_true")
    args = parser.parse_args()
    try:
        (prepare if args.command == "prepare" else execute)(args)
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired) as exc:
        print(type(exc).__name__ + ": " + str(exc), file=sys.stderr)
        sys.exit(1)
