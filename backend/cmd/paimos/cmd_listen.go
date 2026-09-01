// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentmessage"
	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
	"github.com/spf13/cobra"
)

const (
	listenExitNoMessages         = 3
	listenExitAdapterUnavailable = 4
	listenExitForeignWorker      = 5
	listenDefaultPollInterval    = 2 * time.Second
)

type listenExitCode struct {
	code int
	err  error
}

func (e *listenExitCode) Error() string {
	if e.err == nil {
		return "listen stopped"
	}
	return e.err.Error()
}

func (e *listenExitCode) Unwrap() error { return e.err }

type inboxPage struct {
	Address    string            `json:"address"`
	Cursor     int64             `json:"cursor"`
	NextCursor int64             `json:"next_cursor"`
	Messages   []messageEnvelope `json:"messages"`
}

type deliveryOutcome struct {
	EffectiveLevel string
	FallbackReason string
}

func listenCmd() *cobra.Command {
	var (
		projectRef    string
		address       string
		follow        bool
		ack           bool
		deliver       string
		deliverTarget string
		deliverMode   string
		enableGrok    bool
		pollInterval  time.Duration
	)
	c := &cobra.Command{
		Use:   "listen",
		Short: "Read and optionally deliver durable messages for one agent inbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(projectRef) == "" {
				return &usageError{msg: "--project is required"}
			}
			harness, agent, err := splitListenAddress(address)
			if err != nil {
				return &usageError{msg: err.Error()}
			}
			address = harness + ":" + agent
			deliver = strings.ToLower(strings.TrimSpace(deliver))
			if deliver != "" && deliver != "grok" {
				plugin, lookupErr := harnessplugin.Resolve(deliver)
				if lookupErr != nil || plugin.Mode() != harnessplugin.ModeLocal {
					return &usageError{msg: "--deliver must name a registered local harness plugin, a registered alias, or grok"}
				}
			}
			deliverMode = strings.ToLower(strings.TrimSpace(deliverMode))
			if deliverMode == "" {
				deliverMode = "queue"
			}
			if deliverMode != "queue" && deliverMode != "steer" {
				return &usageError{msg: "--deliver-mode must be queue or steer"}
			}
			if pollInterval <= 0 {
				return &usageError{msg: "--poll-interval must be greater than zero"}
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRefToID(client, projectRef)
			if err != nil {
				return reportError(err)
			}
			return runListen(cmd.Context(), client, projectID, address, agent, follow, ack || deliver != "", deliver, strings.TrimSpace(deliverTarget), deliverMode, enableGrok, pollInterval)
		},
	}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&address, "as", "", "receiver address <harness>:<registered-agent> (required)")
	c.Flags().BoolVar(&follow, "follow", false, "keep polling until interrupted")
	c.Flags().BoolVar(&ack, "ack", false, "durably acknowledge messages after output")
	c.Flags().StringVar(&deliver, "deliver", "", "deliver each message through a registered local adapter (codex, agentd_codex, agentd_claude, claude) or grok, then acknowledge")
	c.Flags().StringVar(&deliverTarget, "deliver-target", "", "legacy target for pre-bus messages (Codex thread; Claude local session UUID or session_…/cse_… cloud id; Grok session UUID); bus messages use their receiver-owned target version")
	c.Flags().StringVar(&deliverMode, "deliver-mode", "queue", "legacy pre-bus delivery mode (queue or steer); bus messages use their durable message level, and Claude has no steer primitive so steer falls back to simple")
	c.Flags().BoolVar(&enableGrok, "enable-grok-build-delivery", false, "enable the experimental Grok Build delivery adapter")
	c.Flags().DurationVar(&pollInterval, "poll-interval", listenDefaultPollInterval, "follow polling interval")
	return c
}

func splitListenAddress(raw string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 || !addressPartCLI.MatchString(parts[0]) || !addressPartCLI.MatchString(parts[1]) {
		return "", "", fmt.Errorf("--as must be <harness>:<registered-agent>")
	}
	return strings.ToLower(parts[0]), parts[1], nil
}

var addressPartCLI = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var grokSessionUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func runListen(ctx context.Context, client *Client, projectID int64, address, agent string, follow, acknowledge bool, deliver, target, legacyMode string, enableGrok bool, pollInterval time.Duration) error {
	after, seen := int64(0), false
	worker := workerAdapterFor(deliver)
	for {
		foreignWorker := false
		page, err := fetchInbox(ctx, client, projectID, address, agent, after, worker)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return reportError(err)
		}
		if len(page.Messages) > 0 {
			seen = true
			for _, message := range page.Messages {
				outcome, err := emitOrDeliverMessage(ctx, message, deliver, target, legacyMode, enableGrok)
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					var unavailable *adapterUnavailableError
					if errors.As(err, &unavailable) {
						if unavailable.foreignWorker {
							foreignWorker = true
							break
						}
						if unavailable.reroute && message.DeliveryWork != nil {
							if rerouteErr := rerouteInboxDelivery(ctx, client, projectID, address, agent, message, unavailable.fallbackReason); rerouteErr != nil {
								return reportError(rerouteErr)
							}
							break
						}
						return &listenExitCode{code: listenExitAdapterUnavailable, err: err}
					}
					return err
				}
				after = message.Cursor
				if acknowledge {
					if message.DeliveryWork != nil && deliver != "" {
						if outcome == nil {
							return errors.New("delivery completed without an outcome")
						}
						err = completeInboxDelivery(ctx, client, projectID, address, agent, message, *outcome)
					} else {
						err = ackInbox(ctx, client, projectID, address, agent, after)
					}
					if err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return reportError(err)
					}
				}
			}
		}
		if !follow {
			if foreignWorker {
				return &listenExitCode{code: listenExitForeignWorker, err: errors.New("delivery is pending for another local worker")}
			}
			if !seen {
				return &listenExitCode{code: listenExitNoMessages}
			}
			return nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func rerouteInboxDelivery(ctx context.Context, client *Client, projectID int64, address, agent string, message messageEnvelope, reason string) error {
	if message.DeliveryWork == nil || reason == "" {
		return errors.New("managed delivery reroute requires leased work and a fallback reason")
	}
	_, err := client.doForAgentContext(ctx, "POST", fmt.Sprintf("/api/projects/%d/messages/delivery-unavailable", projectID), map[string]any{
		"to": address, "cursor": message.Cursor, "delivery_id": message.DeliveryWork.DeliveryID, "fallback_reason": reason,
	}, agent)
	return err
}

func completeInboxDelivery(ctx context.Context, client *Client, projectID int64, address, agent string, message messageEnvelope, outcome deliveryOutcome) error {
	_, err := client.doForAgentContext(ctx, "POST", fmt.Sprintf("/api/projects/%d/messages/delivery-complete", projectID), map[string]any{
		"to": address, "cursor": message.Cursor, "delivery_id": message.DeliveryWork.DeliveryID,
		"effective_level": outcome.EffectiveLevel, "fallback_reason": outcome.FallbackReason,
	}, agent)
	return err
}

// workerAdapterFor resolves a CLI alias through the harness registry and asks
// the server to lease work only for receiver-side adapters. The experimental
// Grok Build path is not a durable registry worker.
func workerAdapterFor(deliver string) string {
	plugin, err := harnessplugin.Resolve(deliver)
	if err != nil || plugin.Mode() != harnessplugin.ModeLocal {
		return ""
	}
	return plugin.Name()
}

// fetchInbox reads one attributed inbox page. A non-empty workerAdapter asks
// the server to lease matching receiver-owned delivery work onto the page.
func fetchInbox(ctx context.Context, client *Client, projectID int64, address, agent string, after int64, workerAdapter string) (*inboxPage, error) {
	q := url.Values{"to": []string{address}, "limit": []string{"10"}}
	if after > 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	if workerAdapter != "" {
		q.Set("delivery", workerAdapter)
	}
	raw, err := client.doForAgentContext(ctx, "GET", fmt.Sprintf("/api/projects/%d/messages/listen?%s", projectID, q.Encode()), nil, agent)
	if err != nil {
		return nil, err
	}
	var page inboxPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decode inbox: %w", err)
	}
	return &page, nil
}

func ackInbox(ctx context.Context, client *Client, projectID int64, address, agent string, cursor int64) error {
	_, err := client.doForAgentContext(ctx, "POST", fmt.Sprintf("/api/projects/%d/messages/ack", projectID), map[string]any{
		"to": address, "cursor": cursor,
	}, agent)
	return err
}

func emitOrDeliverMessage(ctx context.Context, message messageEnvelope, deliver, target, legacyMode string, enableGrok bool) (*deliveryOutcome, error) {
	if deliver == "" {
		if flagJSON {
			raw, err := json.Marshal(message)
			if err != nil {
				return nil, err
			}
			_, err = fmt.Fprintln(stdout, string(raw))
			return nil, err
		}
		body := messageText(message)
		_, err := fmt.Fprintf(stdout, "cursor=%d  %s  %s → %s  thread=%s\n%s\n", message.Cursor, message.MessageID, message.From, message.To, message.ThreadID, body)
		return nil, err
	}
	body := messageText(message)
	// Grok Build is an explicitly gated legacy CLI path, separate from the
	// server-owned grok_bot_routine webhook plugin.
	if deliver == "grok" {
		return &deliveryOutcome{EffectiveLevel: "simple"}, deliverGrok(ctx, body, target, enableGrok)
	}
	return deliverHarnessMessage(ctx, message, body, deliver, target, legacyMode)
}

func deliverCodexMessage(ctx context.Context, message messageEnvelope, body, legacyTarget, legacyMode string) (*deliveryOutcome, error) {
	return deliverHarnessMessage(ctx, message, body, agentmessage.AdapterCodex, legacyTarget, legacyMode)
}

func deliverHarnessMessage(ctx context.Context, message messageEnvelope, body, adapter, legacyTarget, legacyMode string) (*deliveryOutcome, error) {
	plugin, err := harnessplugin.Resolve(adapter)
	if err != nil || plugin.Mode() != harnessplugin.ModeLocal {
		return nil, &adapterUnavailableError{message: "UNSUPPORTED: delivery adapter is not a registered local harness plugin"}
	}
	target := legacyTarget
	requested := message.DeliveryLevel
	if requested == "" {
		requested = deliveryLevelFromMode(legacyMode)
	}
	maximum := plugin.MaximumLevel()
	if work := message.DeliveryWork; work != nil {
		switch {
		case work.Adapter == "":
			return nil, &adapterUnavailableError{message: "message has no receiver-owned harness target (delivery " + work.State + "); register a compatible target and requeue"}
		case work.Adapter != plugin.Name():
			return nil, &adapterUnavailableError{message: "message target belongs to another local worker", foreignWorker: true}
		case work.TargetKind != "" && work.TargetKind != plugin.Kind():
			return nil, &adapterUnavailableError{message: "message target kind does not match the selected harness plugin"}
		case work.TargetRef == "":
			return nil, &adapterUnavailableError{message: "message has no usable receiver-owned harness target"}
		}
		target, requested, maximum = work.TargetRef, work.RequestedLevel, work.MaximumLevel
	}
	pluginLevel := requested
	policyCapped := requested == harnessplugin.LevelSteer && maximum == harnessplugin.LevelSimple && plugin.MaximumLevel() == harnessplugin.LevelSteer
	if policyCapped {
		pluginLevel = harnessplugin.LevelSimple
	}
	correlationID := ""
	if message.DeliveryWork != nil {
		correlationID = message.DeliveryWork.DeliveryID
	}
	deliveryRequest := harnessplugin.DeliverRequest{
		Level: pluginLevel, Body: body, TargetRef: target, Stdout: stdout, Stderr: stderr, ClientVersion: Version,
		CorrelationID: correlationID,
	}
	if message.DeliveryWork != nil {
		// Managed controls must carry the exact lease scope into the local
		// supervisor before any vendor primitive can be invoked.
		deliveryRequest.Instance = message.DeliveryWork.Instance
		deliveryRequest.ProjectID = message.DeliveryWork.ProjectID
		deliveryRequest.Identity = message.To
	}
	result, err := harnessplugin.Deliver(ctx, plugin.Name(), deliveryRequest)
	if err != nil {
		return nil, mapHarnessDeliveryError(err)
	}
	if policyCapped {
		result.FallbackReason = "policy_capped"
	}
	rowReason := ""
	if message.DeliveryWork != nil {
		rowReason = message.DeliveryWork.FallbackReason
	}
	result.FallbackReason = chooseDeliveryFallbackReason(result.FallbackReason, rowReason)
	return &deliveryOutcome{EffectiveLevel: result.EffectiveLevel, FallbackReason: result.FallbackReason}, nil
}

func chooseDeliveryFallbackReason(adapterReason, rowReason string) string {
	if adapterReason != "" {
		return adapterReason
	}
	return rowReason
}

func mapHarnessDeliveryError(err error) error {
	var unavailable *harnessplugin.UnavailableError
	if errors.As(err, &unavailable) {
		return &adapterUnavailableError{message: err.Error(), fallbackReason: unavailable.FallbackReason, reroute: unavailable.Reroute}
	}
	if errors.Is(err, harnessplugin.ErrUnsupported) || harnessplugin.ErrorCode(err) != "" {
		return &adapterUnavailableError{message: err.Error()}
	}
	return err
}

func messageText(message messageEnvelope) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Kind == "text" && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

type adapterUnavailableError struct {
	message        string
	fallbackReason string
	reroute        bool
	foreignWorker  bool
}

func (e *adapterUnavailableError) Error() string { return e.message }

// Delivery levels and the Claude delivery-state record.
//
// Claude has no steer primitive: there is no `claude steer`, no
// send-to-session CLI, and no documented messaging-socket user frame. Every
// Claude handoff is therefore effective level `simple`; a `steer` request is
// honored as simple and recorded as fallback_reason=unsupported rather than
// inventing a vendor command.
const (
	deliveryLevelSimple = "simple"
	deliveryLevelSteer  = "steer"

	claudeAdapterResume  = "claude_resume"
	claudeAdapterChannel = "claude_channel"

	claudeFallbackUnsupported = "unsupported"
)

// claudeDelivery is the typed outcome of one Claude handoff. EffectiveLevel
// and FallbackReason are the PAI-826 delivery-state fields (deliveryOutcome);
// Adapter and Primitive are audited locally.
type claudeDelivery struct {
	Adapter        string `json:"adapter"`
	Primitive      string `json:"primitive"`
	RequestedLevel string `json:"requested_level"`
	EffectiveLevel string `json:"effective_level"`
	FallbackReason string `json:"fallback_reason,omitempty"`
}

// claudeSimpleDelivery builds the state record for a Claude adapter. The
// effective level is always simple; a steer request records the fallback.
func claudeSimpleDelivery(adapter, primitive, requestedLevel string) claudeDelivery {
	outcome := claudeDelivery{
		Adapter:        adapter,
		Primitive:      primitive,
		RequestedLevel: normalizeDeliveryLevel(requestedLevel),
		EffectiveLevel: deliveryLevelSimple,
	}
	if outcome.RequestedLevel == deliveryLevelSteer {
		outcome.FallbackReason = claudeFallbackUnsupported
	}
	return outcome
}

// normalizeDeliveryLevel applies the envelope default: anything other than an
// explicit steer request is simple.
func normalizeDeliveryLevel(level string) string {
	if strings.EqualFold(strings.TrimSpace(level), deliveryLevelSteer) {
		return deliveryLevelSteer
	}
	return deliveryLevelSimple
}

// deliveryLevelFromMode maps the legacy process-wide --deliver-mode to a
// requested level. It applies only to pre-bus envelopes without a durable
// delivery_level; bus messages always carry their server-normalized level.
func deliveryLevelFromMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), deliveryLevelSteer) {
		return deliveryLevelSteer
	}
	return deliveryLevelSimple
}

// claudeSessionFlag selects the documented print-mode primitive for a target:
// --resume for a local session UUID, --cloud for a session_…/cse_… cloud id.
// The shape rule is shared with the server-side target registry.
func claudeSessionFlag(target string) (string, bool) {
	return agentmessage.ClaudeSessionPrimitive(target)
}

func deliverCodex(ctx context.Context, body, target string) error {
	_, err := harnessplugin.Deliver(ctx, agentmessage.AdapterCodex, harnessplugin.DeliverRequest{
		Level: harnessplugin.LevelSimple, Body: body, TargetRef: target, Stdout: stdout, Stderr: stderr, ClientVersion: Version,
	})
	return mapHarnessDeliveryError(err)
}

// deliverCodexSteer uses only the documented app-server proxy JSON-RPC
// sequence. A clean idle or rejected/raced steer is returned as a typed simple
// fallback decision; transport and handshake failures degrade to the exact
// queue primitive with the truthful transport_error fallback reason.
func deliverCodexSteer(ctx context.Context, body, target string) (bool, string, error) {
	steered, reason, err := harnessplugin.DeliverCodexSteer(ctx, body, target, stderr, Version)
	return steered, reason, mapHarnessDeliveryError(err)
}

// deliverClaudeMessage resolves the Claude session and the requested level.
// A bus message arrives with leased delivery work: the receiver-owned
// claude_resume target and its durable requested level win, and runListen
// then records the handoff through delivery-complete. A pre-bus envelope
// keeps the legacy --deliver-target session and, without a durable
// delivery_level, the process-wide --deliver-mode. Delivery work that has no
// attached target or that belongs to another adapter (for example the
// receiver's claude_channel push) is never delivered from here.
func deliverClaudeMessage(ctx context.Context, message messageEnvelope, body, legacyTarget, legacyMode string) (*deliveryOutcome, error) {
	return deliverHarnessMessage(ctx, message, body, agentmessage.AdapterClaudeResume, legacyTarget, legacyMode)
}

// deliverClaude hands one framed message to a Claude Code session as a new
// user turn using only the documented print-mode primitives:
//
//   - local idle session: `claude -p --resume <session_uuid>`
//   - cloud session:      `claude -p --cloud <session_id>` (queue-and-exit)
//
// The untrusted body travels over stdin, the documented print-mode prompt
// transport, never through argv or a shell, so a body that starts with "-"
// cannot be parsed as a flag. A zero exit status is the handoff
// acknowledgement; the vendor response is discarded. Claude steer is
// UNSUPPORTED: a steer request falls back to the same simple primitive and is
// recorded as fallback_reason=unsupported. PAIMOS never adds
// --dangerously-skip-permissions, a permission mode, or any other escalation;
// the resumed session keeps its own permission configuration.
func deliverClaude(ctx context.Context, body, target, requestedLevel string) (claudeDelivery, error) {
	requestedLevel = normalizeDeliveryLevel(requestedLevel)
	result, err := harnessplugin.Deliver(ctx, agentmessage.AdapterClaudeResume, harnessplugin.DeliverRequest{
		Level: requestedLevel, Body: body, TargetRef: target, Stdout: stdout, Stderr: stderr, ClientVersion: Version,
	})
	if err != nil {
		return claudeDelivery{}, mapHarnessDeliveryError(err)
	}
	return claudeDelivery{
		Adapter: agentmessage.AdapterClaudeResume, Primitive: result.Primitive, RequestedLevel: requestedLevel,
		EffectiveLevel: result.EffectiveLevel, FallbackReason: result.FallbackReason,
	}, nil
}

func deliverGrok(ctx context.Context, body, target string, enabled bool) error {
	if !enabled {
		return &adapterUnavailableError{message: "Grok Build delivery is experimental; pass --enable-grok-build-delivery"}
	}
	if target == "" {
		target = strings.TrimSpace(os.Getenv("GROK_SESSION_ID"))
	}
	if !grokSessionUUIDPattern.MatchString(target) {
		return &adapterUnavailableError{message: "Grok Build delivery requires a canonical lowercase session UUID via --deliver-target or GROK_SESSION_ID"}
	}
	path, err := exec.LookPath("grok")
	if err != nil {
		return &adapterUnavailableError{message: "Grok Build delivery requires the grok CLI in PATH"}
	}
	// #nosec G204 G702 -- grok is resolved from the operator-controlled PATH;
	// all remaining values are fixed argv entries and no shell is involved.
	cmd := exec.CommandContext(ctx, path,
		"--single", body,
		"--resume", target,
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--tools", "",
		"--no-plan",
		"--no-subagents",
		"--disable-web-search",
		"--max-turns", "1",
		"--verbatim",
	)
	// The vendor response can contain session context. PAIMOS treats a zero exit
	// as the handoff acknowledgement and never captures, stores, or prints it.
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("deliver to Grok Build session: %w", err)
	}
	return nil
}
