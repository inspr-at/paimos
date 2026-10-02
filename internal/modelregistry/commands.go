// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"fmt"
	"strings"
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
		review = run + " --sandbox read-only"
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
