# CI shadow authority (AEON-417 B)

AEON-417 B adds an external **shadow authority** in `scripts/ci-authority` and a
disposable Linux guest init in `scripts/ci-executor`. Install reviewed binaries
outside all candidate workspaces; do not run this controller from a PR checkout.
The authority authenticates the original webhook body with HMAC-SHA256, reads
the numeric repository identity and current main/PR/queue state from GitHub,
and reconstructs the complete plan from its dedicated bare mirror. PR plans
bind the source head and the API-resolved merge commit separately. Queue plans
enumerate every constituent through Git parents and recheck the active queue
ref. Checks are revalidated after reconciliation. Its output includes each
existing required context, every extra inventory context, and `ci/trusted`, all
with **pending** status. There is no check writer or activation flag.

`ci-authority shadow --config /absolute/controller/config.json --event
pull_request --delivery <delivery-id> --signature <X-Hub-Signature-256> --webhook
/absolute/controller/event.json --generation <positive-generation>` reads only
Git objects and GitHub API state. Configuration has schema
`aeon.ci.authority-config.v1`, `mirror`, a `git` object with absolute `path` and
raw SHA-256 `digest`, and `authority`
containing `repository_id`, `repository`, the reviewed `policy` pin,
`environment_digest`, and `verifier_app_id` (zero until provisioned). Supply
`AEON_CI_WEBHOOK_SECRET` and a read-only `AEON_CI_READ_TOKEN` to the controller
process through approved credential storage; neither is forwarded to execution
or output. The installed authority requires a separate Linux host and verifies
root ownership, non-writable parents and Git bytes before opening its mirror.
Mirror refresh is a separate trusted ingress responsibility. The
current replay/generation guard lasts for one controller process; production
needs durable serialized ingress before any authority activation.

`observe` additionally needs a `profile`, `admission_public_key`, signed
`--admission`, `--obligation`, `--run-id`, and `--job-id`. The profile fixes each
obligation's absolute `/opt/aeon/` command, stage, reporter and expected manifest,
plus raw SHA-256 pins for QEMU, firmware, kernel, initrd and a read-only ext4 root
image. It also binds harness/toolchain/environment digests, security epoch,
separate QEMU UID/GID, CPU/memory limits and timeout. The supervisor deep-copies
these settings and requires `infrastructure: hosted-disposable`; the provider
must independently attest that placement. This backend supports Linux/amd64
only and never routes candidates to the trusted main pool or production hosts.
Each metadata/build/test stage boots a new Linux/KVM VM with
read-only framed task/source disks, no network, no host mount and no monitor or
control socket. The root guest init uses a read-only, `nosuid` executor image and
an unprivileged candidate process in private tmpfs storage with a fixed offline
environment. Source export reads every verified Git blob directly, ignores
candidate export attributes, and refuses symlinks/submodules and unsafe paths.
Only bounded content-addressed opaque artifacts can cross the result interface.
Candidate stdout is never interpreted as a host receipt or GitHub check.

Admission is independently Ed25519-signed over the plan, exact candidate commit,
complete tree-manifest digest, policy, executor, approved harness, environment,
epoch, review target/record and a maximum 24-hour validity window. This admits
the complete executable closure, including package initializers, JS helpers,
dependency/config and lifecycle code. It cannot establish honest assertions in
unreviewed code. Supervisor observations have an in-process private seal;
serializing one loses that provenance. Trusted ingress can revoke an admission;
expiry and revocation are rechecked after execution and during reconciliation.
Durable signatures/revocation, source
run/job verification, full receipts and reuse remain E's responsibility. Partial
reruns are refused. The browser/application VM connection and approved browser
reporter remain D/H work and currently fail closed.

This is an unactivated implementation: independently reviewed VM images,
Linux/KVM hosted provisioning and a real boot/tamper probe remain required.
The tests cover protocol, admission and supervisor ownership, including attack
classes from all five AEON-421 reviews; they do not certify a live VM boundary.
App provisioning, expected-App per-context ruleset probes, durable ingress and
review revocation are later coordinator/OPS steps. No workflow, runner route,
required check, version or execution selection changes here. Keep full existing
CI and both optimization switches off until those prerequisites are proven.
Build the guest init with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`; the approved
kernel needs built-in devtmpfs, virtio block/PCI and ext4 support. The pinned
rootfs needs `/workspace`, `/tmp`, `/proc`, `/dev` mountpoints and all approved
tools/dependencies under `/opt/aeon`. No image is produced or provisioned by
this worker, and missing images, recipes or admission refuse execution.
