package main

import (
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

// TestAgentBusRealCodexSimpleE2E is opt-in because it queues a benign message
// into the explicitly supplied real Codex thread. It exercises paimos tell,
// the durable ledger/target/outbox, a live polling listener, the exact queue
// primitive, atomic completion, and cursor acknowledgement.
func TestAgentBusRealCodexSimpleE2E(t *testing.T) {
	threadID := strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_E2E_THREAD"))
	if threadID == "" {
		t.Skip("set PAIMOS_AGENT_BUS_E2E_THREAD to an explicit real Codex thread")
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
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy'),(?,'codex')`, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	service := agentmessage.NewService(paimosdb.DB)
	if err := service.AllowSender(context.Background(), projectID, "codex:codex", "grok_bot:amy"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: "codex", TargetKind: "codex_thread",
		TargetRef: threadID, MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}

	type evidence struct {
		MessageID    string
		DeliveryID   string
		TellStarted  time.Time
		Committed    time.Time
		ListenPicked time.Time
		HandedOff    time.Time
	}
	var proof evidence
	var proofMu sync.Mutex
	firstListen := make(chan struct{}, 1)
	completed := make(chan struct{}, 1)
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
			proof.MessageID = message.MessageID
			proof.Committed = time.Now().UTC()
			proofMu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(message)
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/projects/%d/messages/listen", projectID):
			select {
			case firstListen <- struct{}{}:
			default:
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
				if message.DeliveryWork != nil {
					proofMu.Lock()
					proof.DeliveryID = message.DeliveryWork.DeliveryID
					proof.ListenPicked = time.Now().UTC()
					proofMu.Unlock()
				}
			}
			_ = json.NewEncoder(w).Encode(page)
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
			proof.HandedOff = time.Now().UTC()
			proofMu.Unlock()
			_ = json.NewEncoder(w).Encode(state)
			select {
			case completed <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "e2e-test-key")
	t.Setenv("PAIMOS_AGENT_NAME", "amy")

	listenerCtx, cancelListener := context.WithCancel(context.Background())
	defer cancelListener()
	listenerErr := make(chan error, 1)
	client := &Client{baseURL: server.URL, apiKey: "e2e-test-key", http: server.Client()}
	go func() {
		listenerErr <- runListen(listenerCtx, client, projectID, "codex:codex", "codex", true, true, "codex", "", "queue", false, 20*time.Millisecond)
	}()
	select {
	case <-firstListen:
	case <-time.After(3 * time.Second):
		t.Fatal("live listener did not start")
	}
	proofMu.Lock()
	proof.TellStarted = time.Now().UTC()
	proofMu.Unlock()
	probe := "PAI-826 E2E probe only; no action requested"
	command := tellCmd()
	command.SetArgs([]string{"codex:codex", "--project", "E2E", "--level", "simple", "--message", probe})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(20 * time.Second):
		t.Fatal("Codex handoff did not complete")
	}
	cancelListener()
	if err := <-listenerErr; err != nil {
		t.Fatal(err)
	}
	proofMu.Lock()
	defer proofMu.Unlock()
	if proof.MessageID == "" || proof.DeliveryID == "" || proof.Committed.IsZero() || proof.ListenPicked.IsZero() || proof.HandedOff.IsZero() {
		t.Fatalf("incomplete proof=%#v", proof)
	}
	t.Logf("PAI-826_E2E direction=amy_to_codex message_id=%s delivery_id=%s tell_started=%s committed=%s listen_picked=%s handed_off=%s commit_ms=%d pickup_ms=%d handoff_ms=%d",
		proof.MessageID, proof.DeliveryID, proof.TellStarted.Format(time.RFC3339Nano), proof.Committed.Format(time.RFC3339Nano),
		proof.ListenPicked.Format(time.RFC3339Nano), proof.HandedOff.Format(time.RFC3339Nano),
		proof.Committed.Sub(proof.TellStarted).Milliseconds(), proof.ListenPicked.Sub(proof.Committed).Milliseconds(), proof.HandedOff.Sub(proof.ListenPicked).Milliseconds())
}
