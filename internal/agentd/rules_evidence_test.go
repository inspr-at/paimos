// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
)

// These fixtures model issuance and acknowledgement independently of the old
// worker receipt counter. No model executable or remote service participates.
func (a *managedRulesFixture) PrepareRulesDelivery(_ context.Context, session HarnessSession, in rules.RulesDeliveryPrepare) (rules.RulesDeliveryManifest, error) {
	body, err := assembleRulesPack(a.bundle, in.Repository)
	return rules.RulesDeliveryManifest{ID: "manifest", RunID: in.RunID, SessionID: session.ID, Generation: in.Generation, BodySHA256: rulesDeliveryHash(body), ByteSize: len(body), RepositoryCommit: in.Repository.Commit, ExpiresAt: time.Now().Add(time.Minute)}, err
}
func (a *managedRulesFixture) AcknowledgeRulesDelivery(context.Context, HarnessSession, rules.RulesDeliveryAck) error {
	return nil
}

type evidenceAPI struct {
	*managedRulesFixture
	acknowledged []rules.RulesDeliveryAck
	fail         error
}

func (a *evidenceAPI) AcknowledgeRulesDelivery(_ context.Context, _ HarnessSession, in rules.RulesDeliveryAck) error {
	a.acknowledged = append(a.acknowledged, in)
	return a.fail
}

// Risk: hashing the merge before adding the floor/repo/database framing could
// attest different bytes than the fake bridge actually decoded from the frame.
func TestRulesDeliveryHashMatchesFakeBridgeBytes(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		name := "match"
		vendor := "claude"
		if mismatch {
			name = "mismatch"
			vendor = "claude_rules_mismatch"
		}
		t.Run(name, func(t *testing.T) {
			request := adapterRequest(t)
			request.Profile.Harness = Claude
			merged := rules.Merged{Body: "Database layers.\n", PinnedFiles: []doctrine.PinnedFile{packFile("public", "fixture/company", "docs/AGENTS-KERNEL.md", "Pinned company rules.\n")}}
			repository := repositoryRules{Commit: strings.Repeat("b", 40), Body: "Committed AGENTS.md — UTF-8.\n"}
			body, err := assembleRulesPack(merged, repository)
			if err != nil {
				t.Fatal(err)
			}
			request.Rules = body
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			fake := fakeVendorPath(t, vendor)
			node, sdk := claudeAdapterDependencies(t, fake)
			adapter := NewClaudeAdapter(node, sdk, fake, map[string]string{"account": home})
			api := &evidenceAPI{managedRulesFixture: &managedRulesFixture{bundle: merged}}
			supervisor := &Supervisor{api: api, generation: request.Generation}
			entry := &owned{harness: HarnessSession{ID: "session", ProjectID: "project"}}
			if err = supervisor.prepareRulesDelivery(t.Context(), entry, request.Run, body, repository); err != nil {
				t.Fatal(err)
			}
			if len(api.acknowledged) != 0 {
				t.Fatal("pre-launch issuance reported delivery")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			process, err := adapter.Start(ctx, request, func(AdapterEvent) {})
			if mismatch {
				if !errors.Is(err, ErrRulesDeliveryMismatch) || process != nil || len(api.acknowledged) != 0 {
					t.Fatalf("mismatch accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = process.Stop(context.Background()); _ = process.Wait() }()
			if err = supervisor.acknowledgeRulesDelivery(ctx, entry, process, body); err != nil {
				t.Fatal(err)
			}
			if len(api.acknowledged) != 1 || api.acknowledged[0].BodySHA256 != rulesDeliveryHash(body) || api.acknowledged[0].BridgeSHA256 != api.acknowledged[0].BodySHA256 || api.acknowledged[0].AcknowledgedAt.IsZero() {
				t.Fatal("evidence did not match bridge-received bytes")
			}
			// A metadata write failure is surfaced and fences the process protocol.
			api.fail = errors.New("evidence write failed")
			if err = supervisor.acknowledgeRulesDelivery(ctx, entry, process, body); !errors.Is(err, api.fail) || !errors.Is(err, ErrTelemetryProtocol) {
				t.Fatalf("failed evidence write hidden: %v", err)
			}
		})
	}
}

type deliveryStartProcess struct {
	*fakeProcess
	hash string
}

func (p *deliveryStartProcess) RulesDeliveryHash() string { return p.hash }

type deliveryStartAdapter struct {
	api  *evidenceAPI
	proc *deliveryStartProcess
	t    *testing.T
}

func (*deliveryStartAdapter) Name() string                       { return Claude }
func (*deliveryStartAdapter) Probe(context.Context, string) bool { return true }
func (a *deliveryStartAdapter) Start(_ context.Context, request StartRequest, _ func(AdapterEvent)) (Process, error) {
	if len(a.api.acknowledged) != 0 {
		a.t.Fatal("delivery reported before harness start")
	}
	a.proc.hash = rulesDeliveryHash(request.Rules)
	return a.proc, nil
}

// Risk: a correct helper could still be called before launch or omitted from
// StartRun. The adapter barrier proves the only acknowledgement comes after it.
func TestRulesDeliveryStartRunAcknowledgesAfterHarnessStart(t *testing.T) {
	s, base, process := testPinnedSupervisor(t)
	api := &evidenceAPI{managedRulesFixture: base.managedRulesFixture}
	s.api = api
	base.profile.Harness = Claude
	s.accounts[0].Harness = Claude
	adapter := &deliveryStartAdapter{api: api, proc: &deliveryStartProcess{fakeProcess: process}, t: t}
	s.adapters = map[string]Adapter{Claude: adapter}
	if err := s.StartRun(t.Context(), base.run); err != nil {
		t.Fatal(err)
	}
	if len(api.acknowledged) != 1 || api.acknowledged[0].BridgeSHA256 != adapter.proc.hash {
		t.Fatal("launch acknowledgement missing or wrong")
	}
}
