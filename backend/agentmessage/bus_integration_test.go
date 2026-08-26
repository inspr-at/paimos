package agentmessage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

func openBusTestDB(t *testing.T) (*Service, int64) {
	t.Helper()
	oldDB := paimosdb.DB
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(func() {
		secretvault.ResetForTest()
		if paimosdb.DB != nil {
			_ = paimosdb.DB.Close()
		}
		paimosdb.DB = oldDB
	})
	if err := paimosdb.Open(); err != nil {
		t.Fatal(err)
	}
	project, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Bus','BUS')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender'),(?,'codex'),(?,'amy')`, projectID, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	return NewService(paimosdb.DB), projectID
}

func allowBusSender(t *testing.T, service *Service, projectID int64, receiver string) {
	t.Helper()
	if err := service.AllowSender(context.Background(), projectID, receiver, "paimos:sender"); err != nil {
		t.Fatal(err)
	}
}

func TestBusCodexTargetIdempotencyLeaseAndCompletion(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: "codex", TargetKind: "codex_thread",
		TargetRef: "019d-current-codex-thread", MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "bus observation", DeliveryLevel: "steer", IdempotencyKey: "same-send"}
	first, err := service.SendEnvelope(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SendEnvelope(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.MessageID != first.MessageID || first.DeliveryLevel != "steer" || first.DeliveryFallback != "simple" ||
		first.DeliveryTarget == nil || first.DeliveryTarget.Primary == nil || first.DeliveryTarget.Primary.BindingID != target.ID {
		t.Fatalf("first=%#v replay=%#v target=%#v", first, replay, target)
	}
	conflict := input
	conflict.Body = "different request"
	if _, err := service.SendEnvelope(context.Background(), conflict); err == nil {
		t.Fatal("idempotency conflict was accepted")
	} else if codedErr, ok := err.(*CodedError); !ok || codedErr.Code != "agent_message_idempotency_conflict" {
		t.Fatalf("conflict error=%v", err)
	}
	var messages, deliveries, reservations int
	_ = paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages`).Scan(&messages)
	_ = paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries`).Scan(&deliveries)
	_ = paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_idempotency`).Scan(&reservations)
	if messages != 1 || deliveries != 1 || reservations != 1 {
		t.Fatalf("messages=%d deliveries=%d reservations=%d", messages, deliveries, reservations)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: "codex", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.TargetRef != "019d-current-codex-thread" || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("page=%#v", page)
	}
	work := page.Messages[0].DeliveryWork
	state, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", Cursor: first.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != first.Cursor {
		t.Fatalf("state=%#v", state)
	}
	var deliveryState, effective string
	if err := paimosdb.DB.QueryRow(`SELECT state,effective_level FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&deliveryState, &effective); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "handed_off" || effective != "steer" {
		t.Fatalf("delivery state=%q effective=%q", deliveryState, effective)
	}
	var cipher []byte
	if err := paimosdb.DB.QueryRow(`SELECT target_ref_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), "019d-current-codex-thread") {
		t.Fatal("target reference was stored in plaintext")
	}
}

func TestBusConcurrentIdempotencyCreatesOneMessageAndDelivery(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	input := SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "concurrent replay", IdempotencyKey: "one-logical-send"}
	const callers = 8
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			message, err := service.SendEnvelope(context.Background(), input)
			if err != nil {
				errs <- err
				return
			}
			ids <- message.MessageID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("message ids=%v", unique)
	}
	var messages, deliveries int
	_ = paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages`).Scan(&messages)
	_ = paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries`).Scan(&deliveries)
	if messages != 1 || deliveries != 1 {
		t.Fatalf("messages=%d deliveries=%d", messages, deliveries)
	}
}

func TestBusTargetRegistrationDoesNotImplicitlyReleaseBlockedMessage(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "waiting for target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: "codex", TargetKind: "codex_thread", TargetRef: "registered-later",
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_message_deliveries WHERE message_row_id=?`, message.Cursor).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" {
		t.Fatalf("registration implicitly changed state to %q", state)
	}
	count, err := service.RequeueMissingTargets(context.Background(), projectID, "codex:codex")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: "codex", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.TargetRef != "registered-later" {
		t.Fatalf("page=%#v", page)
	}
}

func TestBusTargetParticipatesInAtomicSecretRotation(t *testing.T) {
	service, projectID := openBusTestDB(t)
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: "codex", TargetKind: "codex_thread", TargetRef: "rotation-thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := paimosdb.DB.QueryRow(`SELECT target_ref_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	newKey := make([]byte, 32)
	for index := range newKey {
		newKey[index] = 7
	}
	report, err := secretvault.Rotate(context.Background(), paimosdb.DB, secretvault.RotateOptions{NewKey: newKey})
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentMessageTargetRows != 1 {
		t.Fatalf("report=%#v", report)
	}
	var after []byte
	if err := paimosdb.DB.QueryRow(`SELECT target_ref_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("rotation did not replace target ciphertext")
	}
	plain, err := secretvault.DecryptWithKey(newKey, "agent-message-targets", after)
	if err != nil || string(plain) != "rotation-thread" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
}

func TestBusWebhookMeasuredHandoffAndSteerFallback(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "grok_bot:amy")
	var received webhookWake
	var idempotency string
	var receivedAt time.Time
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAt = time.Now().UTC()
		idempotency = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook",
		TargetRef: server.URL, MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	tellStarted := time.Now().UTC()
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "grok_bot:amy", Body: "captured webhook payload",
		DeliveryLevel: "steer", IdempotencyKey: "webhook-send",
	})
	if err != nil {
		t.Fatal(err)
	}
	committedAt := time.Now().UTC()
	dispatcher := NewWebhookDispatcher(paimosdb.DB)
	dispatcher.client = server.Client()
	worked, err := dispatcher.DispatchOne(context.Background())
	handedOffAt := time.Now().UTC()
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if received.MessageID != message.MessageID || received.DeliveryID == "" || idempotency != received.DeliveryID ||
		received.RequestedLevel != "steer" || received.EffectiveLevel != "simple" || received.FallbackReason != "unsupported" {
		t.Fatalf("wake=%#v idempotency=%q message=%#v", received, idempotency, message)
	}
	if !strings.Contains(received.Content, "SECURITY NOTICE") || !strings.Contains(received.Content, "captured webhook payload") {
		t.Fatalf("content was not security framed: %q", received.Content)
	}
	var state, effective, reason string
	if err := paimosdb.DB.QueryRow(`SELECT state,effective_level,fallback_reason FROM agent_message_deliveries WHERE delivery_id=?`, received.DeliveryID).Scan(&state, &effective, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "handed_off" || effective != "simple" || reason != "unsupported" {
		t.Fatalf("state=%q effective=%q reason=%q", state, effective, reason)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "grok_bot:amy", Agent: "amy", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Cursor != message.Cursor || len(page.Messages) != 0 {
		t.Fatalf("page=%#v", page)
	}
	t.Logf("PAI-826_E2E direction=codex_to_amy_webhook message_id=%s delivery_id=%s tell_started=%s committed=%s webhook_received=%s handed_off=%s commit_ms=%d wake_ms=%d complete_ms=%d result=success fallback=unsupported effective_level=simple",
		message.MessageID, received.DeliveryID, tellStarted.Format(time.RFC3339Nano), committedAt.Format(time.RFC3339Nano),
		receivedAt.Format(time.RFC3339Nano), handedOffAt.Format(time.RFC3339Nano), committedAt.Sub(tellStarted).Milliseconds(),
		receivedAt.Sub(committedAt).Milliseconds(), handedOffAt.Sub(receivedAt).Milliseconds())
}

func TestWebhookTargetRejectsHTTPAndPrivateAddressByDefault(t *testing.T) {
	service, projectID := openBusTestDB(t)
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	for _, targetRef := range []string{"http://127.0.0.1/hook", "https://127.0.0.1/hook"} {
		_, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
			ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook", TargetRef: targetRef,
		})
		if err == nil {
			t.Fatalf("unsafe webhook %q was accepted", targetRef)
		}
	}
}
