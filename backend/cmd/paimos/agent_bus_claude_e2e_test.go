package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentmessage"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

// TestAgentBusRealClaudeSimpleE2E is opt-in because it resumes the explicitly
// supplied real local Claude Code session with two benign probe turns. It
// exercises paimos tell with a durable level, the ledger, a live polling
// listener, the exact `claude -p --resume <session_id>` primitive with the
// framed body on stdin, and cursor acknowledgement. The second message is a
// steer request and proves the unsupported-steer simple fallback on a real
// session. The session is a receiver-owned claude_resume registry target:
// listen leases the delivery work, resumes the disclosed session, and records
// the handoff through delivery-complete, so the durable delivery row ends
// handed_off instead of blocked/target_missing.
func TestAgentBusRealClaudeSimpleE2E(t *testing.T) {
	sessionID := strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_E2E_CLAUDE_SESSION"))
	if sessionID == "" {
		t.Skip("set PAIMOS_AGENT_BUS_E2E_CLAUDE_SESSION to an explicit real local Claude Code session UUID")
	}
	if flag, ok := claudeSessionFlag(sessionID); !ok || flag != "--resume" {
		t.Fatalf("PAIMOS_AGENT_BUS_E2E_CLAUDE_SESSION must be a lowercase local session UUID")
	}
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	if err := paimosdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = paimosdb.DB.Close()
		paimosdb.DB = nil
	})
	project, _ := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('E2E','E2E')`)
	projectID, _ := project.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex'),(?,'claude')`, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	service := agentmessage.NewService(paimosdb.DB)
	if err := service.AllowSender(context.Background(), projectID, "claude:claude", "paimos:codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:claude", Adapter: agentmessage.AdapterClaudeResume,
		TargetKind: agentmessage.TargetKindClaudeSession, TargetRef: sessionID,
	}); err != nil {
		t.Fatal(err)
	}

	type evidence struct {
		Level        string
		MessageID    string
		Cursor       int64
		TellStarted  time.Time
		Committed    time.Time
		ListenPicked time.Time
		HandedOff    time.Time
	}
	var proofMu sync.Mutex
	proofs := map[int64]*evidence{}
	var pending *evidence
	firstListen := make(chan struct{}, 1)
	acked := make(chan int64, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": projectID, "key": "E2E"}})
		case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/api/projects/%d/messages", projectID):
			var request struct {
				To            string `json:"to"`
				Body          string `json:"body"`
				DeliveryLevel string `json:"delivery_level"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			message, err := service.SendEnvelope(r.Context(), agentmessage.SendEnvelopeInput{
				ProjectID: projectID, Sender: r.Header.Get(agentAttrHeader), To: request.To, Body: request.Body,
				DeliveryLevel: request.DeliveryLevel, IdempotencyKey: r.Header.Get(idempotencyHeader),
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			proofMu.Lock()
			pending.MessageID, pending.Cursor, pending.Committed = message.MessageID, message.Cursor, time.Now().UTC()
			proofs[message.Cursor] = pending
			proofMu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(message)
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/projects/%d/messages/listen", projectID):
			select {
			case firstListen <- struct{}{}:
			default:
			}
			if r.URL.Query().Get("delivery") != agentmessage.AdapterClaudeResume {
				t.Errorf("Claude listener must lease claude_resume delivery work: %q", r.URL.RawQuery)
			}
			after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			page, err := service.ListInbox(r.Context(), agentmessage.InboxInput{
				ProjectID: projectID, Address: r.URL.Query().Get("to"), Agent: r.Header.Get(agentAttrHeader),
				WorkerAdapter: r.URL.Query().Get("delivery"), AfterID: after, Limit: 10,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for index := range page.Messages {
				message := &page.Messages[index]
				message.Parts[0].Text = (agentmessage.FramedMessage{From: message.From, Project: message.ContextID, Hop: message.Hop, Body: message.Parts[0].Text}).FullMessage()
				proofMu.Lock()
				if proof := proofs[message.Cursor]; proof != nil && proof.ListenPicked.IsZero() {
					proof.ListenPicked = time.Now().UTC()
				}
				proofMu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/api/projects/%d/messages/ack", projectID):
			t.Errorf("registry delivery work must complete through delivery-complete, not the plain cursor ack")
			http.Error(w, "unexpected", http.StatusBadRequest)
		case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/api/projects/%d/messages/delivery-complete", projectID):
			var request struct {
				To             string `json:"to"`
				Cursor         int64  `json:"cursor"`
				DeliveryID     string `json:"delivery_id"`
				EffectiveLevel string `json:"effective_level"`
				FallbackReason string `json:"fallback_reason"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, err := service.CompleteLocalDelivery(r.Context(), agentmessage.CompleteDeliveryInput{
				ProjectID: projectID, Address: request.To, Agent: r.Header.Get(agentAttrHeader), Cursor: request.Cursor,
				DeliveryID: request.DeliveryID, EffectiveLevel: request.EffectiveLevel, FallbackReason: request.FallbackReason,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			proofMu.Lock()
			if proof := proofs[request.Cursor]; proof != nil {
				proof.HandedOff = time.Now().UTC()
			}
			proofMu.Unlock()
			_ = json.NewEncoder(w).Encode(state)
			acked <- request.Cursor
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "e2e-test-key")
	t.Setenv("PAIMOS_AGENT_NAME", "codex")

	oldStderr := stderr
	var errOut bytes.Buffer
	stderr = &errOut
	defer func() { stderr = oldStderr }()

	listenerCtx, cancelListener := context.WithCancel(context.Background())
	defer cancelListener()
	listenerErr := make(chan error, 1)
	client := &Client{baseURL: server.URL, apiKey: "e2e-test-key", http: server.Client()}
	go func() {
		// No --deliver-target: the session comes from the leased registry work.
		listenerErr <- runListen(listenerCtx, client, projectID, "claude:claude", "claude", true, true, "claude", "", "queue", false, 20*time.Millisecond)
	}()
	select {
	case <-firstListen:
	case <-time.After(3 * time.Second):
		t.Fatal("live listener did not start")
	}
	var results []*evidence
	for _, level := range []string{"simple", "steer"} {
		proof := &evidence{Level: level}
		proofMu.Lock()
		pending = proof
		proof.TellStarted = time.Now().UTC()
		proofMu.Unlock()
		command := tellCmd()
		command.SetArgs([]string{"claude:claude", "--project", "E2E", "--level", level,
			"--message", "PAI-827 E2E probe (" + level + " level); no action requested, a one-word reply is enough"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		select {
		case cursor := <-acked:
			proofMu.Lock()
			ok := proofs[cursor] == proof
			proofMu.Unlock()
			if !ok {
				t.Fatalf("acknowledged cursor %d is not the %s probe", cursor, level)
			}
		case <-time.After(120 * time.Second):
			t.Fatalf("Claude %s handoff did not complete", level)
		}
		results = append(results, proof)
	}
	cancelListener()
	if err := <-listenerErr; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "steer is unsupported") || !strings.Contains(errOut.String(), "fallback_reason=unsupported") {
		t.Fatalf("steer probe must record the simple fallback, stderr=%q", errOut.String())
	}
	if strings.Count(errOut.String(), "fallback_reason=unsupported") != 1 {
		t.Fatalf("only the steer probe may record a fallback, stderr=%q", errOut.String())
	}
	deliveries, err := service.ListDeliveryStatus(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	proofMu.Lock()
	defer proofMu.Unlock()
	for _, proof := range results {
		if proof.MessageID == "" || proof.Committed.IsZero() || proof.ListenPicked.IsZero() || proof.HandedOff.IsZero() {
			t.Fatalf("incomplete proof=%#v", proof)
		}
		state := "none"
		for _, delivery := range deliveries {
			if delivery.MessageID == proof.MessageID {
				// The registry fold makes the server-side row truthful: the
				// leased claude_resume delivery is handed_off at level simple,
				// with the steer probe recording the unsupported fallback.
				state = delivery.State + "/" + delivery.EffectiveLevel + "/" + delivery.FallbackReason
				wantFallback := ""
				if proof.Level == "steer" {
					wantFallback = "unsupported"
				}
				if delivery.State != "handed_off" || delivery.EffectiveLevel != "simple" || delivery.FallbackReason != wantFallback ||
					delivery.RequestedLevel != proof.Level || delivery.AttemptCount != 1 || delivery.HandedOffAt == "" {
					t.Fatalf("unexpected delivery row for %s probe: %+v", proof.Level, delivery)
				}
			}
		}
		if state == "none" {
			t.Fatalf("%s probe has no delivery row", proof.Level)
		}
		t.Logf("PAI-827_E2E direction=codex_to_claude level=%s primitive=\"claude -p --resume\" message_id=%s cursor=%d delivery_row=%s tell_started=%s committed=%s listen_picked=%s handed_off=%s commit_ms=%d pickup_ms=%d handoff_ms=%d",
			proof.Level, proof.MessageID, proof.Cursor, state, proof.TellStarted.Format(time.RFC3339Nano), proof.Committed.Format(time.RFC3339Nano),
			proof.ListenPicked.Format(time.RFC3339Nano), proof.HandedOff.Format(time.RFC3339Nano),
			proof.Committed.Sub(proof.TellStarted).Milliseconds(), proof.ListenPicked.Sub(proof.Committed).Milliseconds(), proof.HandedOff.Sub(proof.ListenPicked).Milliseconds())
	}
}
