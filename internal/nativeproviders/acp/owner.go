// SPDX-License-Identifier: AGPL-3.0-only

// Package acp implements qualified worker codecs without owning processes.
package acp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	nc "github.com/inspr-at/paimos/internal/nativecontract"
)

const (
	MaxFrameBytes = 1 << 20
	MaxTextBytes  = 64 << 10
)

// Wire is adapted by I2 to the EXISTING owned wire process. Request returns a
// full correlated response. Observe runs synchronously in reader order, before
// any following response is released to Request; callbacks never run in parallel.
// Verify must recheck the actual binary hash, enrolled account, immutable
// tool/settings policy and current grant at the final serialized write. It
// must reject uncertain ownership; public session ids never establish it.
// Close is graceful only; force-stop belongs to the person-authorized owner.
// Implementations must honor deadlines and bound frames before allocating.
type Wire interface {
	// Each write rechecks Verify under the owner's final mutation lock.
	// written, when non-nil, runs once after a complete successful write and
	// BEFORE any response/notification for that request. It is not delivery.
	Request(context.Context, nc.LaunchSpec, json.RawMessage, func()) (json.RawMessage, error)
	Notify(context.Context, nc.LaunchSpec, json.RawMessage) error
	Observe(func(json.RawMessage))
	Identity() nc.NativeIdentity
	Verify(context.Context, nc.LaunchSpec) error
	Close(context.Context) (nc.ExitEvidence, error)
	Wait(context.Context) (nc.ExitEvidence, error)
}

// OpenWire returns a nil wire on error only after proving no possible fork.
// A possible child must return its wire even when startup fails, retaining
// occupancy until the owning supervisor confirms root and group exit.
// OpenWire never selects or pools an account. The owner receives a defensive
// copy of the resolved profile and compiled arguments, not user-provided argv.
type OpenWire func(context.Context, nc.LaunchSpec, []string) (Wire, error)

// Qualification is trusted, local coordinator input after G_PROVIDER_PINS.
// There are deliberately no built-in approved binary pins. Protocol fixtures
// cannot grant authority to a real CLI. The owner enforces both policy digests
// before fork and before writes, including inherited hooks/plugins/MCP tools.
type Qualification struct {
	Harness              string
	Binary               nc.BinaryPin
	ToolPolicyDigest     string
	SettingsPolicyDigest string
}

// MatrixRow separates live persistence from disk resume and turn correlation
// from an explicit publishable final. Force exit is never a codec capability.
type MatrixRow struct {
	Harness          string
	Binary           nc.BinaryPin
	LivePersistence  bool
	Resume           bool
	BusyInput        string
	Approvals        bool
	FinalCorrelation string
	GracefulExit     bool
	ForceExit        bool
	Quota            string
	Confined         bool
	Capabilities     nc.Capabilities
}

func Refuse(reason nc.Reason) error { return &nc.ValidationError{Reason: reason, Path: "$"} }

func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ValidID(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func ValidText(value string) bool {
	return value != "" && len(value) <= MaxTextBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func (q Qualification) Valid(protocol string) bool {
	return filepath.IsAbs(q.Binary.Path) && filepath.Clean(q.Binary.Path) == q.Binary.Path &&
		ValidID(q.Binary.Version, 128) && q.Binary.Protocol == protocol && validHash(q.Binary.SHA256) &&
		validHash(q.ToolPolicyDigest) && validHash(q.SettingsPolicyDigest)
}

func (q Qualification) QualificationDigest(codec string) string {
	return Digest(strings.Join([]string{codec, q.Harness, q.Binary.Path, q.Binary.SHA256,
		q.Binary.Version, q.Binary.Protocol, q.ToolPolicyDigest, q.SettingsPolicyDigest}, "\x00"))
}

func ValidateDTO(schema string, value any) error {
	v, err := nc.NewValidator()
	if err != nil {
		return Refuse(nc.ReasonQualificationMissing)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Refuse(nc.ReasonInvalidInput)
	}
	return v.ValidateJSON(schema, raw)
}

func within(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ValidateSpec accepts native worker authority only. It never reclassifies a
// confined/review/routine grant or treats the native label as a tool bypass.
// Paths are already physical paths from ProfileResolver; the owner verifies
// them again against its enrolled account and approved workspace before fork.
func ValidateSpec(q Qualification, spec nc.LaunchSpec, now time.Time, protocol string) error {
	if !q.Valid(protocol) {
		return Refuse(nc.ReasonQualificationMissing)
	}
	if spec.Profile.Binary != q.Binary {
		return Refuse(nc.ReasonPinInvalid)
	}
	if spec.Profile.SDK != (nc.BinaryPin{}) {
		return Refuse(nc.ReasonUnsupported)
	}
	// Open must return the handle before a caller sends work and consumes its
	// live stream. ACP has no prompt acceptance response before turn completion.
	if spec.InitialInput != nil {
		return Refuse(nc.ReasonUnsupported)
	}
	if !ValidID(spec.Options.Model, 256) || strings.HasPrefix(spec.Options.Model, "-") || spec.Options.Effort != nil && !ValidID(*spec.Options.Effort, 32) {
		return Refuse(nc.ReasonInvalidInput)
	}
	if spec.Mode != "structured" || spec.Binding.RunID == nil || !ValidID(spec.OwnedWriterID, 128) {
		return Refuse(nc.ReasonUnsupported)
	}
	for _, item := range []struct {
		name  string
		value any
	}{
		{"NativeBinding", spec.Binding}, {"NativePolicy", spec.Policy}, {"NativeLaunchConfig", spec.Options},
	} {
		if err := ValidateDTO(item.name, item.value); err != nil {
			return err
		}
	}
	if !now.Before(spec.Policy.ExpiresAt) {
		return Refuse(nc.ReasonGrantExpired)
	}
	if spec.Policy.ToolPolicyDigest != q.ToolPolicyDigest || spec.Policy.SettingsPolicyDigest != q.SettingsPolicyDigest ||
		spec.Options.ToolPolicyDigest != q.ToolPolicyDigest || spec.Options.SettingsPolicyDigest != q.SettingsPolicyDigest {
		return Refuse(nc.ReasonPermissionRevoked)
	}
	if spec.Policy.PermissionPolicy != "supervised" || spec.Policy.LocalHistory != "disabled" ||
		spec.Policy.Continuation != "manual" || spec.Options.ServiceTier != "default" || spec.ResumeRef != "" || len(spec.Tools.Token) != 0 || spec.Tools.URL != "" {
		return Refuse(nc.ReasonUnsupported)
	}
	if !ValidID(spec.Profile.AccountID, 128) || spec.Profile.ProfileBindingRevision < 0 || spec.Lifetime == nil {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	for _, path := range []string{spec.Cwd, spec.Profile.WorkspaceRoot, spec.Profile.AccountHome, spec.Profile.UserHome} {
		if len(path) > 4096 || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return Refuse(nc.ReasonInvalidInput)
		}
	}
	if !within(spec.Profile.WorkspaceRoot, spec.Cwd) || within(spec.Profile.WorkspaceRoot, spec.Profile.AccountHome) || within(spec.Profile.AccountHome, spec.Profile.WorkspaceRoot) {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	if len(spec.Profile.Environment) > 32 {
		return Refuse(nc.ReasonBufferFull)
	}
	seen := map[string]bool{}
	for _, entry := range spec.Profile.Environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || len(entry) > 8192 || seen[key] || strings.ContainsAny(entry, "\x00\r\n") {
			return Refuse(nc.ReasonInvalidInput)
		}
		seen[key] = true
		switch key {
		case "HOME":
			if value != spec.Profile.UserHome {
				return Refuse(nc.ReasonIdentityMismatch)
			}
		case "PATH", "LANG", "LC_ALL", "TMPDIR":
		case "CURSOR_CONFIG_DIR", "GROK_HOME", "GEMINI_CLI_HOME", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME":
			if !filepath.IsAbs(value) || !within(spec.Profile.AccountHome, value) {
				return Refuse(nc.ReasonIdentityMismatch)
			}
		case "GEMINI_CLI_SYSTEM_SETTINGS_PATH":
			if q.Harness != "gemini" || !filepath.IsAbs(value) || !within(spec.Profile.AccountHome, value) {
				return Refuse(nc.ReasonIdentityMismatch)
			}
		default:
			return Refuse(nc.ReasonUnsupported)
		}
	}
	accountKey := map[string]string{"cursor": "CURSOR_CONFIG_DIR", "grok": "GROK_HOME", "gemini": "GEMINI_CLI_HOME", "opencode": "XDG_CONFIG_HOME", "pi": "PI_CODING_AGENT_DIR"}[q.Harness]
	if !seen["HOME"] || !seen[accountKey] {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	return nil
}

func CloneSpec(spec nc.LaunchSpec) nc.LaunchSpec {
	spec.Profile.Environment = append([]string(nil), spec.Profile.Environment...)
	if spec.Binding.RunID != nil {
		id := *spec.Binding.RunID
		spec.Binding.RunID = &id
	}
	if spec.Options.Effort != nil {
		value := *spec.Options.Effort
		spec.Options.Effort = &value
	}
	if spec.InitialInput != nil {
		input := *spec.InitialInput
		if input.ExpectedBinding.RunID != nil {
			id := *input.ExpectedBinding.RunID
			input.ExpectedBinding.RunID = &id
		}
		spec.InitialInput = &input
	}
	return spec
}

func ValidateInput(binding nc.Binding, input nc.Input, now time.Time) error {
	if len(input.Text) > MaxTextBytes {
		return Refuse(nc.ReasonBufferFull)
	}
	if !ValidText(input.Text) {
		return Refuse(nc.ReasonInvalidInput)
	}
	if err := ValidateDTO("NativeInput", input); err != nil {
		return err
	}
	if !binding.Equal(input.ExpectedBinding) {
		return Refuse(nc.ReasonWrongGeneration)
	}
	if !now.Before(input.ExpiresAt) {
		return Refuse(nc.ReasonGrantExpired)
	}
	return nil
}

func CheckWire(ctx context.Context, wire Wire, spec nc.LaunchSpec, expected nc.NativeIdentity, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := spec.Lifetime.Err(); err != nil {
		return Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	if !now.Before(spec.Policy.ExpiresAt) {
		return Refuse(nc.ReasonGrantExpired)
	}
	if !now.Before(expected.StartedAt.Add(time.Duration(spec.Policy.MaximumRuntimeSeconds) * time.Second)) {
		return Refuse(nc.ReasonGrantExpired)
	}
	current := wire.Identity()
	if current.Provider != expected.Provider || current.RuntimeEpoch != expected.RuntimeEpoch || current.PID != expected.PID ||
		current.ProcessIdentityDigest != expected.ProcessIdentityDigest || !current.StartedAt.Equal(expected.StartedAt) {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	if err := wire.Verify(ctx, CloneSpec(spec)); err != nil {
		return Refuse(nc.ReasonPermissionRevoked)
	}
	return nil
}

func ValidateIdentity(identity nc.NativeIdentity, harness string, spec nc.LaunchSpec) error {
	if identity.Provider != harness || identity.RuntimeEpoch != spec.Binding.RuntimeEpoch || identity.PID <= 0 ||
		identity.StartedAt.IsZero() || !validHash(identity.ProcessIdentityDigest) {
		return Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	return nil
}

func ConfirmExit(identity nc.NativeIdentity, exit nc.ExitEvidence, err error) (nc.ExitEvidence, error) {
	if err != nil || exit.ProcessIdentityDigest != identity.ProcessIdentityDigest || !exit.RootExited || !exit.GroupAbsent ||
		exit.ObservedAt.IsZero() || exit.ObservedAt.Before(identity.StartedAt) {
		return exit, Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	return exit, nil
}

// Decode bounds bytes/depth BEFORE building JSON objects. Unknown payload
// fields may be parsed for protocol compatibility but are never forwarded.
func Decode(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || len(raw) > MaxFrameBytes || !utf8.Valid(raw) {
		return Refuse(nc.ReasonBufferFull)
	}
	depth, quoted, escaped := 0, false, false
	for _, c := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 16 {
				return Refuse(nc.ReasonBufferFull)
			}
		case '}', ']':
			depth--
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(dst); err != nil {
		return Refuse(nc.ReasonInvalidInput)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Refuse(nc.ReasonInvalidInput)
	}
	return nil
}
