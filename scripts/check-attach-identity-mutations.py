#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Temporarily remove attach guards; each focused regression must fail.

Run on macOS: GOMAXPROCS=2 nix develop -c python3 scripts/check-attach-identity-mutations.py
Every source file is restored byte-for-byte in finally; no Git operations.
"""
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
os.environ["GOMAXPROCS"] = "2"
MUTATIONS = [
    ("vendor Team ID", "internal/agentd/attach_identity.go",
     'signature.TeamID == "" || signature.TeamID != attachVendorTeam(harness)',
     'false && (signature.TeamID == "" || signature.TeamID != attachVendorTeam(harness))',
     "./internal/agentd", "TestAttachIdentitySecurityChecks/(foreign-team|empty-team)$"),
    ("signature validation", "internal/agentd/attach_signature_darwin.go",
     'run(ctx, "--verify", "--strict", "-v", requirement, pid); err != nil',
     'run(ctx, "--verify", "--strict", "-v", requirement, pid); err != nil && false',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/(invalid-signature|self-signed)$"),
    ("ad-hoc refusal", "internal/agentd/attach_signature_darwin.go",
     'strings.Contains(output, "Signature=adhoc")',
     '(false && strings.Contains(output, "Signature=adhoc"))',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/ad-hoc$"),
    ("strict signature flag", "internal/agentd/attach_signature_darwin.go",
     '"--verify", "--strict", "-v", requirement',
     '"--verify", "-v", requirement',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature"),
    ("Apple certificate chain", "internal/agentd/attach_signature_darwin.go",
     'anchor apple generic', 'always',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature"),
    ("certificate team requirement", "internal/agentd/attach_signature_darwin.go",
     'certificate leaf[subject.OU] = %q', 'certificate leaf[subject.CN] = %q',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/vendor$"),
    ("CLI identifier requirement", "internal/agentd/attach_signature_darwin.go",
     'and identifier %q', 'and identifier != %q',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/vendor$"),
    ("Developer ID marker", "internal/agentd/attach_signature_darwin.go",
     'certificate leaf[field.1.2.840.113635.100.6.1.13] exists',
     'certificate leaf[field.1.2.840.113635.100.6.1.12] exists',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/vendor$"),
    ("Claude Bun refusal", "internal/agentd/attach_signature_darwin.go",
     '\t\tcase claudeRuntimeInjected:\n\t\t\treturn attachSignature{}, errAttachRuntimeDenied\n\t\tcase claudeRuntimeUnobservable:\n\t\t\treturn attachSignature{}, errAttachRuntimeUnobservable',
     '\t\tcase claudeRuntimeInjected:\n\t\t\t_ = decision\n\t\tcase claudeRuntimeUnobservable:\n\t\t\t_ = decision',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/(bun-options|env-unobservable)$"),
    ("Codex skips procargs", "internal/agentd/attach_signature_darwin.go",
     'if identifier == attachVendorIdentifier(Claude) {',
     'if true || identifier == attachVendorIdentifier(Claude) {',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/(codex-bun-options|codex-env-hidden)$"),
    ("argv Bun assignment", "internal/agentd/attach_runtime_darwin.go",
     '\t\tinjected = injected || bunVariableDenied(rest[:i])\n\t\trest = rest[i+1:]\n\t}\n\tif len(rest) == 0 {',
     '\t\trest = rest[i+1:]\n\t}\n\tif len(rest) == 0 {',
     "./internal/agentd", "TestScanAttachProcargs/(empty-argv0|empty-argv|argv-assignment)$"),
    ("trailing Bun assignment", "internal/agentd/attach_runtime_darwin.go",
     '\t\tif i > 0 {\n\t\t\tinjected = injected || bunVariableDenied(rest[:i])\n\t\t}',
     '\t\tif false && i > 0 {\n\t\t\tinjected = injected || bunVariableDenied(rest[:i])\n\t\t}',
     "./internal/agentd", "TestScanAttachProcargs/apple-assignment$"),
    ("truncated procargs string", "internal/agentd/attach_runtime_darwin.go",
     '\tfor len(rest) > 0 {\n\t\ti := bytes.IndexByte(rest, 0)\n\t\tif i < 0 {\n\t\t\treturn 0, errAttachSignatureUnavailable\n\t\t}',
     '\tfor len(rest) > 0 {\n\t\ti := bytes.IndexByte(rest, 0)\n\t\tif i < 0 {\n\t\t\tbreak\n\t\t}',
     "./internal/agentd", "TestScanAttachProcargs/truncated$"),
    ("empty argv padding", "internal/agentd/attach_runtime_darwin.go",
     'for len(rest) > 0 && rest[0] == 0 {',
     'for false && len(rest) > 0 && rest[0] == 0 {',
     "./internal/agentd", "TestScanAttachProcargs/omitted-empty-argv$"),
    ("injection outranks omitted environment", "internal/agentd/attach_runtime_darwin.go",
     '\tif injected {\n\t\treturn claudeRuntimeInjected\n\t}\n\tif !visible {\n\t\treturn claudeRuntimeUnobservable\n\t}',
     '\tif !visible {\n\t\treturn claudeRuntimeUnobservable\n\t}\n\tif injected {\n\t\treturn claudeRuntimeInjected\n\t}',
     "./internal/agentd", "TestScanAttachProcargs/argv-injection-omitted-env$"),
    ("Bun variable predicate", "internal/agentd/attach_runtime_darwin.go",
     '!bytes.HasPrefix(key, []byte("BUN_"))',
     'true',
     "./internal/agentd", "TestScanAttachProcargs/env-assignment$"),
    ("other Bun variables", "internal/agentd/attach_runtime_darwin.go",
     'bytes.HasPrefix(key, []byte("BUN_"))',
     'bytes.Equal(key, []byte("BUN_OPTIONS"))',
     "./internal/agentd", "TestScanAttachProcargs/other-bun$"),
    ("NODE_ stays allowed", "internal/agentd/attach_runtime_darwin.go",
     '!bytes.HasPrefix(key, []byte("BUN_"))',
     '!(bytes.HasPrefix(key, []byte("BUN_")) || bytes.HasPrefix(key, []byte("NODE_")))',
     "./internal/agentd", "TestScanAttachProcargs/node-prefix$"),
    ("BUN_INSTALL allowlist", "internal/agentd/attach_runtime_darwin.go",
     'bytes.Equal(key, []byte("BUN_INSTALL"))',
     'false && bytes.Equal(key, []byte("BUN_INSTALL"))',
     "./internal/agentd", "TestScanAttachProcargs/allowlist$"),
    ("BUN_INSTALL exact name", "internal/agentd/attach_runtime_darwin.go",
     'bytes.Equal(key, []byte("BUN_INSTALL"))',
     'bytes.HasPrefix(key, []byte("BUN_INSTALL"))',
     "./internal/agentd", "TestScanAttachProcargs/allowlist-exact$"),
    ("blank Bun value", "internal/agentd/attach_runtime_darwin.go",
     'len(bytes.TrimSpace(value)) != 0',
     'len(value) != 0',
     "./internal/agentd", "TestScanAttachProcargs/blank$"),
    ("procargs size cap", "internal/agentd/attach_runtime_darwin.go",
     'len(buf) > maxAttachProcargs',
     'len(buf) > maxAttachProcargs && false',
     "./internal/agentd", "TestScanAttachProcargs/oversize$"),
    ("argc cap", "internal/agentd/attach_runtime_darwin.go",
     'argc > 4096',
     'argc > 4096 && false',
     "./internal/agentd", "TestScanAttachProcargs/argc-cap$"),
    ("dynamic PID verification", "internal/agentd/attach_signature_darwin.go",
     '"-v", requirement, pid', '"-v", requirement, strconv.Itoa(n+1)',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/vendor$"),
    ("confirm and poll signature recheck", "internal/agentd/attach_identity.go",
     'func (m *AttachManager) recheckHarnessImage(ctx context.Context, peer, observed attachObservation, id string, s *localAttach) error {',
     'func (m *AttachManager) recheckHarnessImage(ctx context.Context, peer, observed attachObservation, id string, s *localAttach) error { return nil;',
     "./internal/agentd", "TestAttachRechecksRunningSignatureOnConfirmAndPoll"),
    ("startup identity provenance", "cmd/aeon-agentd/attach.go",
     'if derived != nil && *derived == identity {', 'if true { _ = derived;',
     "./cmd/aeon-agentd", "TestAttachStartupDropsOnlyUnverifiableFallbacks"),
    ("startup isolates unavailable fallback", "cmd/aeon-agentd/attach.go",
     'c.AttachIdentities = startupAttachIdentities(c)',
     'previous := c; previous.AttachIdentities = nil; if !validAttachIdentityRefresh(previous, c) { return nil, errors.New("attach fallback differs from the approved installation") }',
     "./cmd/aeon-agentd", "TestAttachStartupRegistersWithUnverifiableFallback"),
    ("unsigned macOS refusal", "internal/agentd/attach_identity.go",
     'if runtime.GOOS != "linux" {', 'if false && runtime.GOOS != "linux" {',
     "./internal/agentd", "TestAttachMacOSRefusesUnsignedImages"),
    ("real unsigned Rosetta refusal", "internal/agentd/attach_identity.go",
     'if runtime.GOOS != "linux" {', 'if false && runtime.GOOS != "linux" {',
     "./internal/agentd", "TestAttachRealUnsignedRosettaImage"),
    ("unsupported Cursor diagnostic", "internal/agentd/attach_identity.go",
     'return &AttachLocalError{Code: "harness_identity_unsupported", Hint: "Cursor attach is unavailable',
     'return &AttachLocalError{Code: "harness_identity_mismatch", Hint: "Cursor attach is unavailable',
     "./internal/agentd", "TestAttachCursorHasNoMacOSVendorIdentity"),
    ("unsupported diagnostic allowlist", "internal/agentdwire/client.go",
     ', "harness_identity_unsupported":', ':',
     "./internal/agentdwire", "TestAttachConflictDiagnosticIsSurfacedAndBounded"),
    ("directory write permissions", "internal/agentsetup/owned_paths.go",
     '// Symlink permission bits are not access controls;',
     'if info.IsDir() { return nil }\n\t// Symlink permission bits are not access controls;',
     "./internal/agentd", "TestAttachIdentitySecurityChecks/writable-directory$"),
    ("recorded owner", "internal/agentsetup/attach_identity.go",
     'int(owner.Uid) != identity.Owner', '(false && int(owner.Uid) != identity.Owner)',
     "./internal/agentsetup", "TestAttachIdentityRecordsNarrowVendorRoot"),
    ("install root boundary", "internal/agentsetup/attach_identity.go",
     '} else if component != target[i] {', '} else if false && component != target[i] {',
     "./internal/agentsetup", "TestAttachIdentityRecordsNarrowVendorRoot"),
    ("exact fallback boundary", "internal/agentsetup/attach_identity.go",
     'if identity.Exact {', 'if false && identity.Exact {',
     "./internal/agentsetup", "TestAttachUnknownLayoutStaysExactAndUnsafeRootIsNotRecorded"),
    ("exact-path owner bypass", "internal/agentd/attach_identity.go",
     'if exists && !identity.Matches(physical, info) || !exists && (pinErr != nil || pinned != physical) {',
     'if !(exists && identity.Matches(physical, info)) && (pinErr != nil || pinned != physical) {',
     "./internal/agentd", "TestAttachLinuxFallbackRejectsWrongOwnerAtExactPin"),
    ("kernel reobservation", "internal/agentd/attach_identity.go",
     'again.Process != observed.Process', '(false && again.Process != observed.Process)',
     "./internal/agentd", "TestAttachIdentitySecurityChecks/kernel-image-change$"),
    ("file replacement check", "internal/agentd/attach_identity.go",
     'return beforeOK && afterOK', 'return true || beforeOK && afterOK',
     "./internal/agentd", "TestAttachRechecksImageOnConfirmAndPoll/.*/replacement$"),
    ("409 diagnostic", "internal/agentdwire/client.go",
     'if path == "/v1/attach" &&', 'if false && path == "/v1/attach" &&',
     "./internal/agentdwire", "TestAttachConflictDiagnosticIsSurfacedAndBounded"),
    ("pairing fallback recording", "internal/agentsetup/setup.go",
     'config.RecordAttachIdentities()', '// config.RecordAttachIdentities()',
     "./internal/agentsetup", "TestAttachIdentityRecordedAtPairingAndRepairWithoutNewEnrollment"),
    ("repair root provenance", "cmd/aeon-agentd/paired_serve.go",
     'func validAttachIdentityRefresh(current, next agentsetup.RuntimeConfig) bool {',
     'func validAttachIdentityRefresh(current, next agentsetup.RuntimeConfig) bool { return true;',
     "./cmd/aeon-agentd", "TestAttachFallbackRefreshCannotWidenApprovedRoot"),
    ("preserved pairing binding", "internal/agentsetup/attach_identity.go",
     'c.ComputerID != previous.ComputerID', '(false && c.ComputerID != previous.ComputerID)',
     "./internal/agentsetup", "TestAttachRecordedRootSurvivesUnavailableOldVersion"),
    ("repair account identity", "internal/agentsetup/lifecycle.go",
     'if current.Path != c.Path || current.Home != c.Home || current.Identity != c.Identity {',
     'if false && (current.Path != c.Path || current.Home != c.Home || current.Identity != c.Identity) {',
     "./internal/agentsetup", "TestAddHarnessRenewsOnlyABlockedPin"),
]

def main():
    if sys.platform != "darwin":
        raise SystemExit("The signature mutations require a macOS test machine.")
    for name, filename, before, after, package, pattern in MUTATIONS:
        path = ROOT / filename
        original = path.read_bytes()
        source = original.decode()
        if source.count(before) != 1:
            raise SystemExit(f"{name}: mutation target is not unique")
        try:
            path.write_text(source.replace(before, after))
            result = subprocess.run(
                ["go", "test", "-p", "2", "-count=1", "-run", pattern, package],
                cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                text=True, timeout=120,
            )
            if result.returncode == 0 or "--- FAIL:" not in result.stdout:
                print(result.stdout[-4000:])
                raise SystemExit(f"{name}: regression did not kill the mutation")
            print(f"KILLED: {name}", flush=True)
        finally:
            path.write_bytes(original)
    print(f"All {len(MUTATIONS)} mutations killed; sources restored.")

if __name__ == "__main__":
    main()
