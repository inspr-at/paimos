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
    ("dynamic PID verification", "internal/agentd/attach_signature_darwin.go",
     '"-v", requirement, pid', '"-v", requirement, strconv.Itoa(n+1)',
     "./internal/agentd", "TestAttachCodesignRequiresValidAppleSignature/vendor$"),
    ("confirm and poll signature recheck", "internal/agentd/attach_identity.go",
     'func (m *AttachManager) recheckHarnessImage(ctx context.Context, peer, observed attachObservation, id string, s *localAttach) error {',
     'func (m *AttachManager) recheckHarnessImage(ctx context.Context, peer, observed attachObservation, id string, s *localAttach) error { return nil;',
     "./internal/agentd", "TestAttachRechecksRunningSignatureOnConfirmAndPoll"),
    ("startup identity provenance", "cmd/aeon-agentd/attach.go",
     'if !validAttachIdentityRefresh(previous, c) {', 'if false && !validAttachIdentityRefresh(previous, c) {',
     "./cmd/aeon-agentd", "TestAttachStartupRejectsUnapprovedFallbackBeforeRegistration"),
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
     "./internal/agentd", "TestAttachExactPinCannotBypassVendorOrRecordedOwner/wrong-recorded-owner$"),
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
