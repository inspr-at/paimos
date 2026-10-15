// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/internal/agentverification"
)

// commandTemplate renders one trusted local command shape. Callers substitute
// {prompt} and {session_id} as single argv values. This function never runs
// a command. review-gate sets readOnly so the run shape is the review shape.
func commandTemplate(harness, model, effort string, readOnly bool) (string, error) {
	run, review, err := harnessShapes(harness)
	if err != nil {
		return "", err
	}
	if readOnly {
		run = review
	}
	replacer := strings.NewReplacer("{model}", model, "{effort}", effort)
	return replacer.Replace(run) + " '{prompt}'", nil
}

func harnessShapes(harness string) (run, review string, err error) {
	switch harness {
	case "codex":
		run = "codex exec -m {model} -c model_reasoning_effort={effort}"
		review = "codex exec --ignore-user-config -s read-only -m {model} -c model_reasoning_effort={effort}"
	case "claude":
		run = "claude -p --model {model} --effort {effort}"
		review = run + " --permission-mode plan"
	case "grok":
		run = "grok --model {model} --reasoning-effort {effort} -p"
		review = "grok --model {model} --reasoning-effort {effort} --permission-mode plan -p"
	case "pi":
		run = "pi --model {model}:{effort} -p"
		review = "pi --model {model}:{effort} --tools read,grep,find,ls -p"
	case "cursor":
		run = "cursor-agent --trust --model {model} -p"
		review = "cursor-agent --trust --mode ask --model {model} -p"
	case "gemini", "opencode":
		run = "paimos harness invoke --harness " + harness + " --model {model} --effort {effort} --"
		review = "paimos harness invoke --harness " + harness + " --model {model} --effort {effort} --review --"
	default:
		return "", "", fmt.Errorf("unsupported command harness %q", harness)
	}
	return run, review, nil
}

// ValidateReviewCommand binds Codex qualification to the exact printed command.
// Read-only sandboxing alone leaves inherited MCP servers and hooks enabled.
// This does not widen the pairing/daemon verification adapter's qualification.
func ValidateReviewCommand(harness, model, effort, command string) error {
	if harness != "codex" {
		return nil
	}
	if len(model) > 128 || !modelRE.MatchString(model) || len(effort) > 32 || !effortRE.MatchString(effort) {
		return fmt.Errorf("invalid Codex review pin")
	}
	expected, err := commandTemplate(harness, model, effort, true)
	if err != nil || command != expected {
		return fmt.Errorf("Codex review requires the exact isolated command with --ignore-user-config and -s read-only")
	}
	return nil
}

func reviewCommandCapability(p Profile) agentverification.Capability {
	if p.Harness == "codex" {
		command, err := commandTemplate(p.Harness, p.Model, p.Effort, true)
		if err == nil {
			err = ValidateReviewCommand(p.Harness, p.Model, p.Effort, command)
		}
		if err != nil {
			return agentverification.Capability{Reason: err.Error()}
		}
		return agentverification.Capability{Supported: true, Policy: "isolated_read_only"}
	}
	return agentverification.For(p.Harness, "darwin", "arm64")
}
