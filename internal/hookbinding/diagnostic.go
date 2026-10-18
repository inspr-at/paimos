// SPDX-License-Identifier: AGPL-3.0-only

// Package hookbinding keeps connection diagnostics value-free. Refusals must
// never forward a worker lease, credential, transcript path or remote body.
package hookbinding

type Refusal struct {
	Code  string `json:"code"`
	Cause error  `json:"-"`
}

func (e *Refusal) Error() string { return Reason(e.Code) }
func (e *Refusal) Unwrap() error { return e.Cause }

func Reason(code string) string {
	switch code {
	case "instance_mismatch":
		return "The paired daemon uses a different instance. Use the same instance for the heartbeat and daemon."
	case "principal_mismatch":
		return "The paired daemon uses a different principal. The generic inbox hook must use the heartbeat's CLI and instance."
	case "worker_proof_rejected":
		return "The server rejected the paired daemon's principal, project or worker lease for this registration."
	case "hook_cli_mismatch":
		return "The inbox hook uses a different or older CLI. Reinstall it using the heartbeat's CLI and --instance with hook install --harness claude --scope project."
	case "hook_instance_mismatch":
		return "The inbox hook uses a different instance. Reinstall it with the heartbeat's --instance."
	case "hook_unavailable":
		return "The generic Claude inbox hook is missing or cannot be verified. Install it with the heartbeat's CLI and instance."
	case "hook_index_mismatch":
		return "The source-session index does not bind this live generation. Restart its heartbeat with the same --source-session."
	case "daemon_unavailable":
		return "The paired daemon is unavailable or too old to bind this hook. Update or restart agentd."
	case "process_unverified":
		return "The daemon could not verify the existing Claude process and its heartbeat helper."
	case "harness_identity_mismatch":
		return "The running Claude executable does not have the required trusted vendor identity."
	case "generation_mismatch":
		return "The paired hook belongs to a different process, project or lease generation."
	case "pairing_unavailable":
		return "An active pairing for this session's principal is required by the daemon."
	case "daemon_refused":
		return "The paired daemon refused the hook binding. Run aeon doctor with the heartbeat's instance."
	}
	return ""
}

func Valid(code string) bool { return Reason(code) != "" }
