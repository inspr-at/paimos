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

	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

type durableThirdAdapterStub struct{}

func (durableThirdAdapterStub) Name() string         { return "third_adapter" }
func (durableThirdAdapterStub) Kind() string         { return "third_ref" }
func (durableThirdAdapterStub) MaximumLevel() string { return harnessplugin.LevelSimple }
func (durableThirdAdapterStub) Mode() string         { return harnessplugin.ModeLocal }
func (durableThirdAdapterStub) ValidateTarget(context.Context, string) error {
	return nil
}
func (durableThirdAdapterStub) Deliver(context.Context, harnessplugin.DeliverRequest) (harnessplugin.DeliverResult, error) {
	return harnessplugin.DeliverResult{EffectiveLevel: harnessplugin.LevelSimple}, nil
}

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

func TestBusPersistsRegisteredThirdAdapterTargetWithoutCoreChanges(t *testing.T) {
	if err := harnessplugin.Register(durableThirdAdapterStub{}); err != nil {
		t.Fatal(err)
	}
	service, projectID := openBusTestDB(t)
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "third:amy", Adapter: "third_adapter", TargetKind: "third_ref",
		TargetRef: "safe-third-party-reference", MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.Adapter != "third_adapter" || target.TargetKind != "third_ref" {
		t.Fatalf("target=%#v", target)
	}
	targets, err := service.ListTargets(context.Background(), projectID, "third:amy")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Adapter != "third_adapter" || targets[0].TargetKind != "third_ref" {
		t.Fatalf("targets=%#v", targets)
	}
	listed, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listed), "target_ref") || strings.Contains(string(listed), "safe-third-party-reference") {
		t.Fatalf("target list exposed a secret reference: %s", listed)
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
	if work.Instance != "ppm" || work.ProjectID != projectID {
		t.Fatalf("delivery scope=%q/%d want ppm/%d", work.Instance, work.ProjectID, projectID)
	}
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

func TestBusCodexTransportFallbackCompletion(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: "codex", TargetKind: "codex_thread",
		TargetRef: "019d-transport-codex-thread", MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "transport fallback", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: "codex", Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	work := page.Messages[0].DeliveryWork
	if _, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "simple", FallbackReason: "transport_error",
	}); err != nil {
		t.Fatal(err)
	}
	var state, effective, reason string
	if err := paimosdb.DB.QueryRow(`SELECT state,effective_level,fallback_reason FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&state, &effective, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "handed_off" || effective != "simple" || reason != "transport_error" {
		t.Fatalf("state=%q effective=%q reason=%q", state, effective, reason)
	}
}

func TestBusAgentdManagedSteerUsesLeaseAndCanonicalCompletion(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	targetRef := `{"socket":"/tmp/paimos-agentd-test.sock","session_id":"019d1234-1234-7123-8123-123456789abc"}`
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterAgentdCodex,
		TargetKind: TargetKindAgentdSession, TargetRef: targetRef, MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex,
		TargetKind: TargetKindCodexThread, TargetRef: "codex-simple-thread", MaximumLevel: "simple", Role: "simple_fallback",
	}); err != nil {
		t.Fatal(err)
	}
	simple, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "simple inbox", DeliveryLevel: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	simplePage, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: AdapterCodex, Limit: 10,
	})
	if err != nil || len(simplePage.Messages) != 1 || simplePage.Messages[0].DeliveryWork == nil {
		t.Fatalf("simple page=%#v err=%v", simplePage, err)
	}
	simpleWork := simplePage.Messages[0].DeliveryWork
	if simpleWork.Adapter != AdapterCodex || simpleWork.TargetRef != "codex-simple-thread" || simpleWork.State != "leased" {
		t.Fatalf("simple work=%#v", simpleWork)
	}
	if _, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", Cursor: simple.Cursor,
		DeliveryID: simpleWork.DeliveryID, EffectiveLevel: "simple",
	}); err != nil {
		t.Fatal(err)
	}
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "managed steer", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	work := page.Messages[0].DeliveryWork
	if work.State != "leased" || work.DeliveryID == "" || work.TargetRef != targetRef {
		t.Fatalf("work=%#v", work)
	}
	if _, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer", FallbackReason: "",
	}); err != nil {
		t.Fatal(err)
	}
	var state, effective, fallback, handedOff string
	if err := paimosdb.DB.QueryRow(`SELECT state,effective_level,fallback_reason,handed_off_at FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&state, &effective, &fallback, &handedOff); err != nil {
		t.Fatal(err)
	}
	if state != "handed_off" || effective != "steer" || fallback != "" || handedOff == "" {
		t.Fatalf("state=%q effective=%q fallback=%q handed_off_at=%q", state, effective, fallback, handedOff)
	}
}

func TestBusAgentdClaudeSteerCarriesDurableMessageAndDeliveryIDs(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "claude:codex")
	targetRef := `{"socket":"/tmp/paimos-agentd-claude.sock","session_id":"019d1234-1234-7123-8123-123456789abc"}`
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "claude:codex", Adapter: AdapterAgentdClaude,
		TargetKind: TargetKindAgentdSession, TargetRef: targetRef, MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "claude:codex", Body: "owned Claude steer", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "claude:codex", Agent: "codex", WorkerAdapter: AdapterAgentdClaude, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	work := page.Messages[0].DeliveryWork
	if message.MessageID == "" || work.DeliveryID == "" || work.Adapter != AdapterAgentdClaude ||
		work.TargetRef != targetRef || work.RequestedLevel != "steer" || work.State != "leased" {
		t.Fatalf("message=%#v work=%#v", message, work)
	}
	if _, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "claude:codex", Agent: "codex", Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBusTargetSelectorPreservesLegacySimpleCappedSteerFallback(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex,
		TargetKind: TargetKindCodexThread, TargetRef: "primary-simple", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex,
		TargetKind: TargetKindCodexThread, TargetRef: "fallback-simple", MaximumLevel: "simple", Role: "simple_fallback",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "legacy steer", DeliveryLevel: "steer",
	}); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: AdapterCodex, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil ||
		page.Messages[0].DeliveryWork.TargetRef != "fallback-simple" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestBusTargetSelectorKeepsOrdinarySimpleOnPrimary(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex,
		TargetKind: TargetKindCodexThread, TargetRef: "primary-steer", MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex,
		TargetKind: TargetKindCodexThread, TargetRef: "fallback-simple", MaximumLevel: "simple", Role: "simple_fallback",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "ordinary simple", DeliveryLevel: "simple",
	}); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: AdapterCodex, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil ||
		page.Messages[0].DeliveryWork.TargetRef != "primary-steer" {
		t.Fatalf("page=%#v err=%v", page, err)
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
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	webhook, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook",
		TargetRef: "https://127.0.0.1/hook", TargetSecret: busTestSenderKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	var before, secretBefore []byte
	if err := paimosdb.DB.QueryRow(`SELECT target_ref_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT target_secret_cipher FROM agent_message_targets WHERE id=?`, webhook.ID).Scan(&secretBefore); err != nil {
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
	if report.AgentMessageTargetRows != 2 || report.AgentMessageTargetSecretRows != 1 {
		t.Fatalf("report=%#v", report)
	}
	var after, secretAfter []byte
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
	if err := paimosdb.DB.QueryRow(`SELECT target_secret_cipher FROM agent_message_targets WHERE id=?`, webhook.ID).Scan(&secretAfter); err != nil {
		t.Fatal(err)
	}
	if string(secretBefore) == string(secretAfter) {
		t.Fatal("rotation did not replace the sender secret ciphertext")
	}
	secretPlain, err := secretvault.DecryptWithKey(newKey, "agent-message-target-secrets", secretAfter)
	if err != nil || string(secretPlain) != busTestSenderKey {
		t.Fatalf("secret rotation err=%v matches=%v", err, string(secretPlain) == busTestSenderKey)
	}
	if _, err := secretvault.DecryptWithKey(newKey, "agent-message-targets", secretAfter); err == nil {
		t.Fatal("sender secret ciphertext must not verify under the target-reference domain")
	}
}

func TestBusWebhookMeasuredHandoffAndSteerFallback(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "grok_bot:amy")
	var received webhookWake
	var idempotency, authorization string
	var receivedAt time.Time
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAt = time.Now().UTC()
		idempotency = r.Header.Get("Idempotency-Key")
		authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook",
		TargetRef: server.URL, TargetSecret: busTestSenderKey, MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !target.HasSecret {
		t.Fatalf("target=%#v, want has_secret", target)
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
	if authorization != "Bearer "+busTestSenderKey {
		t.Fatalf("Authorization header=%q, want the routine sender key as a bearer token", authorization)
	}
	if strings.Contains(received.Content, busTestSenderKey) || strings.Contains(received.Content, server.URL) {
		t.Fatal("wake payload leaked the capability URL or sender key")
	}
	var secretCipher []byte
	if err := paimosdb.DB.QueryRow(`SELECT target_secret_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&secretCipher); err != nil {
		t.Fatal(err)
	}
	if len(secretCipher) <= 28 || strings.Contains(string(secretCipher), busTestSenderKey) {
		t.Fatal("sender key was not stored as ciphertext")
	}
	listed, err := service.ListTargets(context.Background(), projectID, "grok_bot:amy")
	if err != nil || len(listed) != 1 || !listed[0].HasSecret {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	listedJSON, _ := json.Marshal(listed)
	if strings.Contains(string(listedJSON), busTestSenderKey) || strings.Contains(string(listedJSON), server.URL) || strings.Contains(string(listedJSON), "target_secret") {
		t.Fatalf("target list exposed a secret: %s", listedJSON)
	}
	statuses, err := service.ListDeliveryStatus(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, _ := json.Marshal(statuses)
	if strings.Contains(string(statusJSON), busTestSenderKey) || strings.Contains(string(statusJSON), server.URL) {
		t.Fatalf("delivery status exposed a secret: %s", statusJSON)
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
	for targetRef, wantCode := range map[string]string{
		"http://127.0.0.1/hook":  "agent_message_target_webhook_invalid",
		"https://127.0.0.1/hook": "agent_message_target_webhook_address_denied",
	} {
		_, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
			ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook",
			TargetRef: targetRef, TargetSecret: busTestSenderKey,
		})
		if codedErrorCode(err) != wantCode {
			t.Fatalf("unsafe webhook %q err=%v want %s", targetRef, err, wantCode)
		}
	}
}

const busTestSenderKey = "crsr_fixture_sender_key_0001"

// TestBusWebhookTargetSenderSecretPolicy proves the sender key is required
// exactly where the adapter sends one, refused everywhere else, validated
// without being echoed, and never visible through any read surface.
func TestBusWebhookTargetSenderSecretPolicy(t *testing.T) {
	service, projectID := openBusTestDB(t)
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	for _, tc := range []struct {
		name, adapter, kind, ref, secret, wantCode string
	}{
		{"webhook without sender key", "grok_bot_routine", "https_webhook", "https://127.0.0.1/hook", "", "agent_message_target_secret_required"},
		{"webhook with prebuilt header value", "grok_bot_routine", "https_webhook", "https://127.0.0.1/hook", "Bearer " + busTestSenderKey, "agent_message_target_secret_invalid"},
		{"webhook with whitespace in key", "grok_bot_routine", "https_webhook", "https://127.0.0.1/hook", "crsr_bad key", "agent_message_target_secret_invalid"},
		{"codex with sender key", "codex", "codex_thread", "019d-codex-thread", busTestSenderKey, "agent_message_target_secret_unsupported"},
		{"webhook with raw sender key", "grok_bot_routine", "https_webhook", "https://127.0.0.1/hook", busTestSenderKey, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
				ProjectID: projectID, Address: "grok_bot:amy", Adapter: tc.adapter, TargetKind: tc.kind, TargetRef: tc.ref, TargetSecret: tc.secret,
			})
			if codedErrorCode(err) != tc.wantCode {
				t.Fatalf("code=%q err=%v want %q", codedErrorCode(err), err, tc.wantCode)
			}
			if err != nil && tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("error echoed the sender key: %v", err)
			}
			if tc.wantCode == "" && (target == nil || !target.HasSecret) {
				t.Fatalf("target=%#v", target)
			}
		})
	}
	var count int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_targets WHERE target_secret_cipher IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored secrets=%d err=%v", count, err)
	}
}

// TestWebhookDispatcherEmptyQueueLeavesRequestWritesWritable proves an idle
// poll stays read-only while representative authenticated-request writes are
// in flight. Production opens SQLite with _txlock=immediate, so a BeginTx here
// would wait behind the request writer even though there is no webhook work to
// lease, joining the writer queue that it used to starve every 250ms.
func TestWebhookDispatcherEmptyQueueLeavesRequestWritesWritable(t *testing.T) {
	_, _ = openBusTestDB(t)
	var journalMode string
	if err := paimosdb.DB.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode=%q, want wal", journalMode)
	}
	user, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('poll-writer','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := user.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	key, err := paimosdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'poll-writer','hash','prefix','issues:read')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := key.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	writer, err := paimosdb.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), `ROLLBACK`)

	started := make(chan struct{})
	type dispatchResult struct {
		worked bool
		err    error
	}
	result := make(chan dispatchResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	go func() {
		close(started)
		worked, dispatchErr := NewWebhookDispatcher(paimosdb.DB).DispatchOne(ctx)
		result <- dispatchResult{worked: worked, err: dispatchErr}
	}()
	<-started

	// These mirror ResolveAPIKey's write-throttled usage stamp and
	// SessionAuditMiddleware's mutation audit insert. They must remain usable
	// while the empty dispatcher poll is running.
	if _, err := writer.ExecContext(context.Background(), `UPDATE api_keys SET last_used_at=datetime('now') WHERE id=?`, keyID); err != nil {
		t.Fatalf("api-key usage stamp was not writable: %v", err)
	}
	if _, err := writer.ExecContext(context.Background(), `INSERT INTO session_activity(session_id,user_id,method,path,status_code) VALUES(NULL,?,'POST','/api/issues',201)`, userID); err != nil {
		t.Fatalf("session activity was not writable: %v", err)
	}

	dispatched := <-result
	if dispatched.err != nil || dispatched.worked {
		t.Fatalf("empty poll joined the writer queue: worked=%v err=%v", dispatched.worked, dispatched.err)
	}
}

// TestBusWebhookLegacyTargetWithoutSenderSecretFailsClosed proves a pre-M157
// webhook target version is never dispatched without its sender key: the
// endpoint is not contacted and the delivery blocks with a typed reason.
func TestBusWebhookLegacyTargetWithoutSenderSecretFailsClosed(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "grok_bot:amy")
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook",
		TargetRef: server.URL, TargetSecret: busTestSenderKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_message_targets SET target_secret_cipher=NULL WHERE id=?`, target.ID); err != nil {
		t.Fatal(err)
	}
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "grok_bot:amy", Body: "legacy wake"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := NewWebhookDispatcher(paimosdb.DB)
	dispatcher.client = server.Client()
	worked, err := dispatcher.DispatchOne(context.Background())
	if !worked || err == nil {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if calls != 0 {
		t.Fatalf("endpoint was contacted %d times without a sender key", calls)
	}
	var state, code string
	if err := paimosdb.DB.QueryRow(`SELECT d.state,d.last_error_code FROM agent_message_deliveries d JOIN agent_messages am ON am.id=d.message_row_id WHERE am.message_id=?`, message.MessageID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || code != "target_secret_missing" {
		t.Fatalf("state=%q code=%q", state, code)
	}
	// A new version registered with the sender key wakes the next message.
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: "grok_bot_routine", TargetKind: "https_webhook",
		TargetRef: server.URL, TargetSecret: busTestSenderKey,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_message_deliveries SET state='dead'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "grok_bot:amy", Body: "authenticated wake"}); err != nil {
		t.Fatal(err)
	}
	worked, err = dispatcher.DispatchOne(context.Background())
	if !worked || err != nil || calls != 1 {
		t.Fatalf("worked=%v err=%v calls=%d", worked, err, calls)
	}
}

const (
	busTestClaudeLocalSession = "8f3c2a1e-4b6d-4c8e-9a1f-0d2e3f4a5b6c"
	busTestClaudeCloudSession = "session_01DiUkqY2kzbUbDmW1w96rfi"
)

func addBusClaudeReceiver(t *testing.T, service *Service, projectID int64) {
	t.Helper()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'claude')`, projectID); err != nil {
		t.Fatal(err)
	}
	allowBusSender(t, service, projectID, "claude:claude")
}

func codedErrorCode(err error) string {
	if coded, ok := err.(*CodedError); ok {
		return coded.Code
	}
	return ""
}

// TestBusClaudeResumeTargetLeaseAndCompletion proves the PAI-827 registry
// fold: a receiver-owned claude_resume session target is snapshotted onto the
// message, disclosed only to a claude_resume worker, and completed as a simple
// handoff with the unsupported-steer fallback recorded durably.
func TestBusClaudeResumeTargetLeaseAndCompletion(t *testing.T) {
	service, projectID := openBusTestDB(t)
	addBusClaudeReceiver(t, service, projectID)
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "claude:claude", Adapter: "claude_resume", TargetKind: "claude_session",
		TargetRef: busTestClaudeLocalSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.Adapter != AdapterClaudeResume || target.TargetKind != TargetKindClaudeSession || target.MaximumLevel != "simple" || !target.Enabled || target.Version != 1 {
		t.Fatalf("target=%#v", target)
	}
	first, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "claude:claude", Body: "claude observation", DeliveryLevel: "steer"})
	if err != nil {
		t.Fatal(err)
	}
	if first.DeliveryTarget == nil || first.DeliveryTarget.Primary == nil || first.DeliveryTarget.Primary.BindingID != target.ID || first.DeliveryTarget.Primary.Kind != TargetKindClaudeSession {
		t.Fatalf("snapshot=%#v", first.DeliveryTarget)
	}

	// A Codex worker sees redacted state only: no lease, no session reference.
	page, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "claude:claude", Agent: "claude", WorkerAdapter: "codex", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "pending" ||
		page.Messages[0].DeliveryWork.Adapter != AdapterClaudeResume || page.Messages[0].DeliveryWork.TargetRef != "" {
		t.Fatalf("codex worker page=%#v", page.Messages)
	}

	// The claude_resume worker leases the row and receives the decrypted session.
	page, err = service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "claude:claude", Agent: "claude", WorkerAdapter: "claude_resume", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil {
		t.Fatalf("claude worker page=%#v", page.Messages)
	}
	work := page.Messages[0].DeliveryWork
	if work.State != "leased" || work.Adapter != AdapterClaudeResume || work.TargetKind != TargetKindClaudeSession ||
		work.TargetRef != busTestClaudeLocalSession || work.RequestedLevel != "steer" || work.MaximumLevel != "simple" {
		t.Fatalf("work=%#v", work)
	}

	// Claude has no steer primitive: a steer completion is refused durably.
	_, err = service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "claude:claude", Agent: "claude", Cursor: first.Cursor, DeliveryID: work.DeliveryID, EffectiveLevel: "steer",
	})
	if codedErrorCode(err) != "agent_message_effective_level_invalid" {
		t.Fatalf("steer completion err=%v", err)
	}
	state, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "claude:claude", Agent: "claude", Cursor: first.Cursor, DeliveryID: work.DeliveryID,
		EffectiveLevel: "simple", FallbackReason: "unsupported",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != first.Cursor {
		t.Fatalf("state=%#v", state)
	}
	var deliveryState, effective, fallback string
	if err := paimosdb.DB.QueryRow(`SELECT state,effective_level,fallback_reason FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&deliveryState, &effective, &fallback); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "handed_off" || effective != "simple" || fallback != "unsupported" {
		t.Fatalf("delivery state=%q effective=%q fallback=%q", deliveryState, effective, fallback)
	}
	statuses, err := service.ListDeliveryStatus(context.Background(), projectID)
	if err != nil || len(statuses) != 1 || statuses[0].State != "handed_off" || statuses[0].EffectiveLevel != "simple" || statuses[0].FallbackReason != "unsupported" {
		t.Fatalf("statuses=%#v err=%v", statuses, err)
	}
	var cipher []byte
	if err := paimosdb.DB.QueryRow(`SELECT target_ref_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), busTestClaudeLocalSession) {
		t.Fatal("Claude session reference was stored in plaintext")
	}
	// The handed-off row is no longer offered to any worker.
	page, err = service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "claude:claude", Agent: "claude", WorkerAdapter: "claude_resume", Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("page after completion=%#v err=%v", page, err)
	}
}

func TestBusClaudeChannelTargetLeasesOnlyForChannelWorker(t *testing.T) {
	service, projectID := openBusTestDB(t)
	addBusClaudeReceiver(t, service, projectID)
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "claude:claude", Adapter: "claude_channel", TargetKind: "claude_session",
		TargetRef: busTestClaudeLocalSession,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "claude:claude", Body: "channel push"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "claude:claude", Agent: "claude", WorkerAdapter: "claude_resume", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "pending" ||
		page.Messages[0].DeliveryWork.Adapter != AdapterClaudeChannel || page.Messages[0].DeliveryWork.TargetRef != "" {
		t.Fatalf("resume worker must not lease a channel target: %#v", page.Messages)
	}
	page, err = service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "claude:claude", Agent: "claude", WorkerAdapter: "claude_channel", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "leased" ||
		page.Messages[0].DeliveryWork.TargetRef != busTestClaudeLocalSession || page.Messages[0].DeliveryWork.RequestedLevel != "simple" {
		t.Fatalf("channel worker page=%#v", page.Messages)
	}
	if _, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "claude:claude", Agent: "claude", Cursor: first.Cursor,
		DeliveryID: page.Messages[0].DeliveryWork.DeliveryID, EffectiveLevel: "simple",
	}); err != nil {
		t.Fatal(err)
	}
	var deliveryState string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_message_deliveries WHERE delivery_id=?`, page.Messages[0].DeliveryWork.DeliveryID).Scan(&deliveryState); err != nil || deliveryState != "handed_off" {
		t.Fatalf("state=%q err=%v", deliveryState, err)
	}
}

func TestBusClaudeTargetRegistrationValidation(t *testing.T) {
	service, projectID := openBusTestDB(t)
	addBusClaudeReceiver(t, service, projectID)
	for _, tc := range []struct {
		name, adapter, kind, ref, level, wantCode string
	}{
		{"resume with codex kind", "claude_resume", "codex_thread", busTestClaudeLocalSession, "simple", "agent_message_target_kind_invalid"},
		{"channel with webhook kind", "claude_channel", "https_webhook", busTestClaudeLocalSession, "simple", "agent_message_target_kind_invalid"},
		{"socket path is not a session", "claude_resume", "claude_session", "/tmp/claude-messaging.sock", "simple", "agent_message_target_ref_invalid"},
		{"url is not a session", "claude_resume", "claude_session", "https://claude.ai/code/session_x", "simple", "agent_message_target_ref_invalid"},
		{"session name is not a session", "claude_resume", "claude_session", "my-session", "simple", "agent_message_target_ref_invalid"},
		{"uppercase uuid is not canonical", "claude_resume", "claude_session", strings.ToUpper(busTestClaudeLocalSession), "simple", "agent_message_target_ref_invalid"},
		{"channel needs a local session", "claude_channel", "claude_session", busTestClaudeCloudSession, "simple", "agent_message_target_ref_invalid"},
		{"claude has no steer policy", "claude_resume", "claude_session", busTestClaudeLocalSession, "steer", "agent_message_target_level_invalid"},
		{"cli adapter name is not a registry adapter", "claude", "claude_session", busTestClaudeLocalSession, "simple", "agent_message_target_adapter_invalid"},
		{"resume accepts a local session", "claude_resume", "claude_session", busTestClaudeLocalSession, "", ""},
		{"resume accepts a cloud session", "claude_resume", "claude_session", busTestClaudeCloudSession, "simple", ""},
		{"channel accepts a local session", "claude_channel", "claude_session", busTestClaudeLocalSession, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
				ProjectID: projectID, Address: "claude:claude", Adapter: tc.adapter, TargetKind: tc.kind, TargetRef: tc.ref, MaximumLevel: tc.level,
			})
			if got := codedErrorCode(err); got != tc.wantCode {
				t.Fatalf("code=%q err=%v want %q", got, err, tc.wantCode)
			}
			if tc.wantCode == "" && (target == nil || target.Adapter != tc.adapter || target.TargetKind != TargetKindClaudeSession || target.MaximumLevel != "simple") {
				t.Fatalf("target=%#v", target)
			}
		})
	}
}

func TestBusListInboxRejectsNonLocalWorkerAdapters(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	for _, worker := range []string{"grok_bot_routine", "claude", "grok", "CODEX"} {
		_, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: worker, Limit: 10})
		if codedErrorCode(err) != "agent_message_worker_adapter_invalid" {
			t.Fatalf("worker %q err=%v", worker, err)
		}
	}
	if _, err := service.ListInbox(context.Background(), InboxInput{ProjectID: projectID, Address: "codex:codex", Agent: "codex", Limit: 10}); err != nil {
		t.Fatalf("plain read must stay valid: %v", err)
	}
}

func TestClaudeSessionPrimitiveShapes(t *testing.T) {
	for ref, want := range map[string]string{
		busTestClaudeLocalSession:              "--resume",
		busTestClaudeCloudSession:              "--cloud",
		"cse_abc-DEF_123":                      "--cloud",
		"session_":                             "",
		"/tmp/claude.sock":                     "",
		"ws://localhost:1234":                  "",
		"8F3C2A1E-4B6D-4C8E-9A1F-0D2E3F4A5B6C": "",
		"":                                     "",
	} {
		flag, ok := ClaudeSessionPrimitive(ref)
		if flag != want || ok != (want != "") {
			t.Fatalf("ClaudeSessionPrimitive(%q)=%q,%v want %q", ref, flag, ok, want)
		}
	}
}
