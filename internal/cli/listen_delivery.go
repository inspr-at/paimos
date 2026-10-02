// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/version"
)

type localDeliveryResult struct {
	EffectiveLevel string
	FallbackReason string
}

type localDeliverer func(context.Context, string, inbox.DeliveryWork) (localDeliveryResult, error)

type localUnavailable struct{ reason string }

func (e *localUnavailable) Error() string { return "local delivery unavailable" }

func (rt *runtime) doMessaging(ctx context.Context, method, path string, body, dest any) error {
	c, err := rt.api()
	if err != nil {
		return err
	}
	if err := c.Do(ctx, method, path, body, dest); err != nil {
		return rt.fail(err, c.Token)
	}
	return nil
}

func localMessagingAdapter(name string) bool {
	switch name {
	case "codex", "agentd_codex", "agentd_claude", "agentd_pi", "agentd_cursor", "agentd_gemini", "agentd_opencode", "claude_resume", "claude_channel", "claude":
		return true
	}
	return false
}

func canonicalMessagingAdapter(name string) string {
	if name == "claude" {
		return "claude_resume"
	}
	return name
}

func (rt *runtime) listenMessagingDelivery(project, projectKey, address, adapter string, follow bool, interval time.Duration) error {
	adapter = canonicalMessagingAdapter(adapter)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	deliver := rt.messagingDeliverer
	if deliver == nil {
		deliver = deliverLocalMessaging
	}
	seen := false
	initialBackoff := min(interval, 30*time.Second)
	backoff := initialBackoff
	retry := func() bool {
		waited := waitMessagingRetry(ctx, backoff)
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
		return waited
	}
	for {
		var work inbox.DeliveryWork
		if err := rt.doMessaging(ctx, http.MethodPost, "/api/projects/"+url.PathEscape(project)+"/messages/delivery-claim", map[string]string{"to": address, "adapter": adapter}, &work); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if work.ID == "" {
			if !follow {
				if !seen {
					return &exitError{code: 3}
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
				continue
			}
		}
		switch work.State {
		case "foreign_worker", "leased":
			if follow {
				if !retry() {
					return nil
				}
				continue
			}
			return &exitError{code: 5, msg: "delivery is pending for another local worker"}
		case "blocked":
			if follow {
				if !retry() {
					return nil
				}
				continue
			}
			return &exitError{code: 4, msg: "message has no usable receiver-owned harness target"}
		case "pending":
		default:
			return &exitError{code: 4, msg: "message delivery is unavailable"}
		}
		if work.Message == nil || work.LeaseToken == "" || work.TargetRef == "" {
			return &exitError{code: 4, msg: "incomplete receiver-owned delivery work"}
		}
		work.ProjectKey = projectKey
		result, err := deliver(ctx, adapter, work)
		if err != nil {
			var unavailable *localUnavailable
			if errors.As(err, &unavailable) {
				req := map[string]string{"delivery_id": work.ID, "lease_token": work.LeaseToken, "fallback_reason": unavailable.reason}
				rerouteErr := rt.doMessaging(ctx, http.MethodPost, "/api/projects/"+url.PathEscape(project)+"/messages/delivery-unavailable", req, nil)
				if follow {
					if !retry() {
						return nil
					}
					continue
				}
				if rerouteErr == nil {
					return &exitError{code: 5, msg: "delivery rerouted to its receiver-owned fallback worker"}
				}
				return &exitError{code: 4, msg: "local delivery unavailable; no usable fallback target"}
			}
			if follow {
				if !retry() {
					return nil
				}
				continue
			}
			return &exitError{code: 4, msg: "local delivery failed: " + redact(err.Error(), work.TargetRef)}
		}
		if result.EffectiveLevel != "simple" && result.EffectiveLevel != "steer" {
			return &exitError{code: 4, msg: "local adapter returned an invalid delivery level"}
		}
		if work.FallbackReason != "" {
			result.FallbackReason = work.FallbackReason
		}
		request := map[string]string{"delivery_id": work.ID, "lease_token": work.LeaseToken, "effective_level": result.EffectiveLevel, "fallback_reason": result.FallbackReason}
		completeCtx, cancelComplete := context.WithTimeout(context.Background(), 30*time.Second)
		err = rt.doMessaging(completeCtx, http.MethodPost, "/api/projects/"+url.PathEscape(project)+"/messages/delivery-complete", request, nil)
		cancelComplete()
		if err != nil {
			return err
		}
		seen = true
		backoff = initialBackoff
		if !follow {
			return nil
		}
	}
}

// The adapter receives a receiver-owned reference from an authenticated claim
// or an explicit local session binding. Vendor output is discarded because it can echo the
// message or reference. A nonzero exit is never treated as a handoff.
func deliverLocalMessaging(ctx context.Context, adapter string, work inbox.DeliveryWork) (localDeliveryResult, error) {
	if work.Message == nil {
		return localDeliveryResult{}, errors.New("message missing")
	}
	level := work.Message.Level
	body := messagingFrame(*work.Message, work.ProjectKey)
	if level == "" {
		level = "simple"
	}
	result := localDeliveryResult{EffectiveLevel: "simple"}
	if level == "steer" && work.MaximumLevel == "simple" {
		result.FallbackReason = "policy_capped"
	}
	switch adapter {
	case "codex":
		if level == "steer" && work.MaximumLevel == "steer" {
			steered, reason, err := DeliverCodexSteer(ctx, body, work.TargetRef, io.Discard, version.Version)
			if err != nil {
				return localDeliveryResult{}, errors.New("Codex steer failed")
			}
			if steered {
				return localDeliveryResult{EffectiveLevel: "steer"}, nil
			}
			result.FallbackReason = reason
		}
		return result, runLocalMessagingCommand(ctx, "codex", []string{"queue", "--thread", work.TargetRef, "--message", body}, nil)
	case "claude_resume":
		flag := "--resume"
		if strings.HasPrefix(work.TargetRef, "session_") || strings.HasPrefix(work.TargetRef, "cse_") {
			flag = "--cloud"
		}
		if level == "steer" && result.FallbackReason == "" {
			result.FallbackReason = "unsupported"
		}
		return result, runLocalMessagingCommand(ctx, "claude", []string{"-p", flag, work.TargetRef}, strings.NewReader(body))
	case "agentd_codex", "agentd_claude", "agentd_pi", "agentd_cursor", "agentd_gemini", "agentd_opencode":
		return deliverAgentdMessaging(ctx, adapter, work)
	case "claude_channel":
		return localDeliveryResult{}, &localUnavailable{reason: "not_steerable"}
	default:
		return localDeliveryResult{}, errors.New("unregistered local adapter")
	}
}

func runLocalMessagingCommand(ctx context.Context, program string, args []string, input io.Reader) error {
	path, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("%s CLI unavailable", program)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s delivery failed", program)
	}
	return nil
}
