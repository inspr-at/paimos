// SPDX-License-Identifier: AGPL-3.0-only

package nativecontract

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// DTOs above are the wire truth; this port is independent of agentd to avoid import cycles.
type Host interface {
	Start(context.Context, LocalStart) (OperationResult, error)
	Resume(context.Context, ResumeIntent) (OperationResult, error)
	Input(context.Context, Input) (OperationResult, error)
	Interrupt(context.Context, Interrupt) (OperationResult, error)
	Stop(context.Context, Stop) (OperationResult, error)
	Attach(context.Context, AttachIntent) (AttachResult, error)
	Renew(context.Context, AttachRenew) (AttachResult, error)
	Detach(context.Context, Detach) (OperationResult, error)
	Take(context.Context, TakeIntent) (OperationResult, error)
	Return(context.Context, ReturnIntent) (OperationResult, error)
	PTYInput(context.Context, PTYInput) (OperationResult, error)
	Resize(context.Context, Resize) (OperationResult, error)
	Decide(context.Context, ApprovalDecision) (OperationResult, error)
	EndControl(context.Context, EndControl) (OperationResult, error)
	EventsPage(context.Context, Binding, EventQuery) (EventPage, error)
	Snapshot(context.Context, Binding) (SessionView, error)
	Receipts(context.Context, Binding, PageQuery) (ReceiptPage, error)
	Subscribe(context.Context, Binding, StreamQuery) (Subscription, error)
}
type Subscription interface {
	Events() <-chan StreamEvent
	Close() error
}

// Actor is immutable authenticated identity supplied by the transport, not a DTO field.
// Transactor takes tenant -> tree -> target locks, invokes RequireTx and appends events last.
type Store interface {
	Start(context.Context, Actor, StartIntent) (OperationResult, error)
	Mutate(context.Context, Actor, Command) (OperationResult, error)
	Read(context.Context, Actor, Binding) (SessionView, error)
	Claim(context.Context, DaemonActor, Claim) (ClaimResult, error)
	Commands(context.Context, DaemonActor, PageQuery) (PendingCommandPage, error)
	Report(context.Context, DaemonActor, Receipt) (OperationResult, error)
	Receipts(context.Context, Actor, Binding, PageQuery) (ReceiptPage, error)
}
type FrameSource interface {
	Subscribe(context.Context, Binding) (Subscription, error)
}
type ProfileResolver interface {
	Resolve(context.Context, ProfileRequest) (LocalProfile, error)
}
type NativeTransport interface {
	Send(context.Context, Input) (NativeEvidence, error)
	Interrupt(context.Context, Interrupt) (NativeEvidence, error)
	Decide(context.Context, ApprovalDecision) (NativeEvidence, error)
	Close(context.Context) (ExitEvidence, error)
	Wait(context.Context) (ExitEvidence, error)
	Capabilities() Capabilities
	Identity() NativeIdentity
	Events() <-chan ProviderEvent
}

// LocalProfile/LaunchSpec/NativeIdentity contain local-only account home, physical cwd,
// pinned binary, policy and process proof; they never serialize into public DTOs.
type Launcher interface {
	Open(context.Context, LaunchSpec) (NativeTransport, error)
}
type OwnershipProbe interface {
	Observe(context.Context, WriterRecord) (ExitEvidence, error)
}
type FinalSink interface {
	Persist(context.Context, FinalDelivery) (FinalReceipt, error)
}
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

// Internal types are never JSON/public projections. C0 supplies the concrete definitions:
type Actor struct {
	TenantID, PrincipalID, OwnerPersonID, PrincipalKind, GrantID string
	KeyCeiling                                                   []string
	GrantRevision                                                int64
}
type DaemonActor struct {
	Actor
	ComputerID, DaemonID, DaemonGeneration, LedgerGeneration, CapabilityDigest string
}
type PageQuery struct {
	After string
	Limit int
}
type StreamQuery struct {
	After, View string
	Generation  int64
}
type EventQuery struct {
	Before     string
	Limit      int
	Generation int64
}
type Command struct {
	Kind       string
	Input      *Input
	Interrupt  *Interrupt
	Stop       *Stop
	Attach     *AttachIntent
	Renew      *AttachRenew
	Detach     *Detach
	Take       *TakeIntent
	Return     *ReturnIntent
	Approval   *ApprovalDecision
	Resume     *ResumeIntent
	EndControl *EndControl
}
type BinaryPin struct {
	Path, SHA256, Version, Protocol string
}
type ProfileRequest struct {
	TenantID, OwnerPersonID, AccountID, AccountKey, WorkspaceID, Harness string
	Policy                                                               Policy
	BindingRevision                                                      int64
}
type LocalProfile struct {
	AccountID, AccountHome, UserHome, WorkspaceRoot, ConversationRoot string
	Environment                                                       []string
	Binary, SDK                                                       BinaryPin
	ProfileBindingRevision                                            int64
}
type ToolCapability struct {
	URL       string
	Token     []byte // existing run-bound tool proof only, never daemon/vendor credential
	Binding   Binding
	ExpiresAt time.Time
}
type LaunchSpec struct {
	Binding                             Binding
	Profile                             LocalProfile
	Policy                              Policy
	Cwd, ResumeRef, Mode, OwnedWriterID string
	Options                             LaunchConfig
	InitialInput                        *Input
	Tools                               ToolCapability
	Lifetime                            context.Context
}
type NativeIdentity struct {
	Provider, NativeSessionRef, NativeThreadRef, NativeTurnRef, ResumeCursor string
	RuntimeEpoch, ProcessIdentityDigest                                      string
	PID                                                                      int
	StartedAt                                                                time.Time
}
type NativeEvidence struct {
	Stage, InputID, TurnRef, RequestID, EffectDigest string
	SourceSequence                                   uint64
	ObservedAt                                       time.Time
}
type ProviderEvent struct {
	SourceSequence uint64
	Identity       NativeIdentity
	Structured     *StructuredFrame
	Expert         *ExpertFrame
	Receipt        *Receipt
	Approval       *Approval
	Quota          *Quota
	Final          *FinalPublish
	BackgroundWork bool
}
type WriterRecord struct {
	Binding                    Binding
	Identity                   NativeIdentity
	AttemptID                  string
	PossibleFork, ExitObserved bool
}
type ExitEvidence struct {
	ProcessIdentityDigest   string
	RootExited, GroupAbsent bool
	ObservedAt              time.Time
	Reason                  string
}
type FinalDelivery = FinalPublish
type FinalReceipt = NativeFinalReceipt

// C0 generation map: NativeBinding -> Binding, NativePolicy -> Policy, other
// Native-prefixed public DTOs strip Native, except NativeFinalReceipt above.
// Host/Store methods reject unknown typed Command kinds and nil required arms.
// Local-only types refuse wire serialization; token byte slices are zeroed on close.
// Module.NewDisabled mounts the authored routes with feature_disabled/503.
// Module.New(Services) is enabled only with a qualified Store/FrameSource/FinalSink;
// I1/I2 alone wire Services in cmd/aeon/serve.go and agentd's owning supervisor.
type Services struct {
	Store     Store
	Frames    FrameSource
	Finals    FinalSink
	Qualified bool
}

// PTY is a separate owner transport. The structured SDK/app-server never shares
// stdin or a live native conversation writer with this runtime.
type PTYLaunchSpec struct {
	LaunchSpec
	Rows, Cols int
}
type PTYRuntime interface {
	Identity() NativeIdentity
	Write(context.Context, PTYInput) (NativeEvidence, error)
	Resize(context.Context, Resize) (NativeEvidence, error)
	Frames() <-chan TerminalFrame
	Close(context.Context) (ExitEvidence, error)
	Wait(context.Context) (ExitEvidence, error)
}
type PTYLauncher interface {
	OpenPTY(context.Context, PTYLaunchSpec) (PTYRuntime, error)
}

// This port holds the existing owned writer lock across the entire transition.
// PrepareSuccessor obtains a fresh CURRENT server grant/admission; confirmed
// old root/group exit is required before it can open a successor.
type WriterCoordinator interface {
	Quiesce(context.Context, Binding, string) (WriterRecord, error)
	CloseStructured(context.Context, WriterRecord) (ExitEvidence, error)
	ConfirmExit(context.Context, WriterRecord, ExitEvidence) error
	PrepareSuccessor(context.Context, Actor, Binding, string) (LaunchSpec, error)
	CommitRuntime(context.Context, Binding, NativeIdentity, string) (Binding, error)
}

// Output is already filtered by provider-specific allowlists before FrameSource.
// Both local and public transport independently enforce the same export rules.

// localOnly is embedded in internal authority/process/profile types. They must
// never become JSON projections, including when nested in another value.
type localOnly struct{}

func (localOnly) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native local authority cannot be serialized")
}
func (*localOnly) UnmarshalJSON([]byte) error {
	return errors.New("native local authority cannot be decoded from JSON")
}

// Close erases only this run-bound tool proof; it never handles vendor credentials.
func (c *ToolCapability) Close() error { clear(c.Token); c.Token = nil; return nil }

func (Actor) MarshalJSON() ([]byte, error)              { return localOnly{}.MarshalJSON() }
func (*Actor) UnmarshalJSON(data []byte) error          { return (&localOnly{}).UnmarshalJSON(data) }
func (DaemonActor) MarshalJSON() ([]byte, error)        { return localOnly{}.MarshalJSON() }
func (*DaemonActor) UnmarshalJSON(data []byte) error    { return (&localOnly{}).UnmarshalJSON(data) }
func (BinaryPin) MarshalJSON() ([]byte, error)          { return localOnly{}.MarshalJSON() }
func (*BinaryPin) UnmarshalJSON(data []byte) error      { return (&localOnly{}).UnmarshalJSON(data) }
func (ProfileRequest) MarshalJSON() ([]byte, error)     { return localOnly{}.MarshalJSON() }
func (*ProfileRequest) UnmarshalJSON(data []byte) error { return (&localOnly{}).UnmarshalJSON(data) }
func (LocalProfile) MarshalJSON() ([]byte, error)       { return localOnly{}.MarshalJSON() }
func (*LocalProfile) UnmarshalJSON(data []byte) error   { return (&localOnly{}).UnmarshalJSON(data) }
func (ToolCapability) MarshalJSON() ([]byte, error)     { return localOnly{}.MarshalJSON() }
func (*ToolCapability) UnmarshalJSON(data []byte) error { return (&localOnly{}).UnmarshalJSON(data) }
func (LaunchSpec) MarshalJSON() ([]byte, error)         { return localOnly{}.MarshalJSON() }
func (*LaunchSpec) UnmarshalJSON(data []byte) error     { return (&localOnly{}).UnmarshalJSON(data) }
func (NativeIdentity) MarshalJSON() ([]byte, error)     { return localOnly{}.MarshalJSON() }
func (*NativeIdentity) UnmarshalJSON(data []byte) error { return (&localOnly{}).UnmarshalJSON(data) }
func (NativeEvidence) MarshalJSON() ([]byte, error)     { return localOnly{}.MarshalJSON() }
func (*NativeEvidence) UnmarshalJSON(data []byte) error { return (&localOnly{}).UnmarshalJSON(data) }
func (ProviderEvent) MarshalJSON() ([]byte, error)      { return localOnly{}.MarshalJSON() }
func (*ProviderEvent) UnmarshalJSON(data []byte) error  { return (&localOnly{}).UnmarshalJSON(data) }
func (WriterRecord) MarshalJSON() ([]byte, error)       { return localOnly{}.MarshalJSON() }
func (*WriterRecord) UnmarshalJSON(data []byte) error   { return (&localOnly{}).UnmarshalJSON(data) }
func (ExitEvidence) MarshalJSON() ([]byte, error)       { return localOnly{}.MarshalJSON() }
func (*ExitEvidence) UnmarshalJSON(data []byte) error   { return (&localOnly{}).UnmarshalJSON(data) }
func (PTYLaunchSpec) MarshalJSON() ([]byte, error)      { return localOnly{}.MarshalJSON() }
func (*PTYLaunchSpec) UnmarshalJSON(data []byte) error  { return (&localOnly{}).UnmarshalJSON(data) }

var _ json.Marshaler = Actor{}
var _ json.Marshaler = LocalProfile{}
var _ json.Marshaler = LaunchSpec{}
var _ json.Marshaler = PTYLaunchSpec{}
