// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func repinClaude(ctx context.Context, engine *agentsetup.Engine, discovery agentsetup.Discovery, pins agentsetup.ClaudeDependencies, prompt setupPrompt, yes bool) error {
	plan, err := engine.PrepareClaudeRepin(ctx, discovery, pins)
	if err != nil {
		return err
	}
	if prompt.json {
		err = json.NewEncoder(prompt.out).Encode(struct {
			Stage string                     `json:"stage"`
			Plan  agentsetup.ClaudeRepinPlan `json:"changes"`
		}{"repin_preview", plan})
	} else {
		_, err = fmt.Fprintf(prompt.out, "Claude dependency changes (saved path → resolved path; version):\nNode: %q → %q (%s)\n   →  %q → %q (%s)\nSDK:  %q → %q (%s)\n   →  %q → %q (%s)\n", plan.Old.NodePath, plan.Old.NodeResolved, plan.Old.NodeVersion, plan.New.NodePath, plan.New.NodeResolved, plan.New.NodeVersion, plan.Old.SDKPath, plan.Old.SDKResolved, plan.Old.SDKVersion, plan.New.SDKPath, plan.New.SDKResolved, plan.New.SDKVersion)
	}
	if err != nil {
		return err
	}
	if !yes {
		yes, err = prompt.confirm("Apply these pins and restart Claude when its runs are idle?", "--yes")
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("repin declined; saved dependencies were preserved")
		}
	}
	id, err := engine.ApplyClaudeRepin(ctx, plan)
	if err != nil {
		return err
	}
	if err := printSetupProgress(prompt.out, prompt.json, agentsetup.Progress{Stage: "repin_pending", Action: "Dependency change recorded. Waiting for the paired daemon to restart its idle Claude adapter; active runs and service ownership are preserved."}); err != nil {
		return err
	}
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		applied, err := agentsetup.ClaudeRepinApplied(engine.Store, id)
		if err != nil {
			return err
		}
		if applied {
			return printSetupProgress(prompt.out, prompt.json, agentsetup.Progress{Stage: "repinned", Action: "Claude adapter restarted with the checked dependencies. Local repin event: " + id})
		}
		select {
		case <-op.Done():
			return errors.New("repin saved; restart remains pending until Claude runs settle and the paired daemon is running. Start an offline daemon through its existing service owner (Nix/Home Manager when managed); no re-pairing is needed")
		case <-ticker.C:
		}
	}
}
