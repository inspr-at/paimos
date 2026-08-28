// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	AdapterGrokBotRoutine = "grok_bot_routine"
	KindHTTPSWebhook      = "https_webhook"
)

type GrokRoutinePlugin struct{}

func (GrokRoutinePlugin) Name() string         { return AdapterGrokBotRoutine }
func (GrokRoutinePlugin) Kind() string         { return KindHTTPSWebhook }
func (GrokRoutinePlugin) MaximumLevel() string { return LevelSimple }
func (GrokRoutinePlugin) Mode() string         { return ModeServer }

func (GrokRoutinePlugin) ValidateTarget(ctx context.Context, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return &Error{Code: CodeWebhookInvalid, Message: "webhook target must be an HTTPS URL without userinfo or fragment"}
	}
	if !WebhookHostAllowed(parsed.Hostname()) {
		return &Error{Code: CodeWebhookHostDenied, Message: "webhook hostname is not in PAIMOS_AGENT_BUS_WEBHOOK_HOSTS"}
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return &Error{Code: CodeWebhookDNSFailed, Message: "webhook hostname did not resolve"}
	}
	for _, address := range addresses {
		if !WebhookIPAllowed(address.IP) {
			return &Error{Code: CodeWebhookAddressDenied, Message: "webhook hostname resolves to a denied address"}
		}
	}
	return nil
}

func (GrokRoutinePlugin) Deliver(context.Context, DeliverRequest) (DeliverResult, error) {
	return DeliverResult{}, &UnavailableError{Message: "grok_bot_routine delivery is owned by the server webhook dispatcher"}
}

// SecretHeader names the verified Grok Bot routine webhook contract: the
// routine's "When a webhook fires" trigger card issues a POST URL, a sender
// key, and the ready-made `Authorization: Bearer <sender key>` header. PAIMOS
// stores only the raw sender key and renders the header at dispatch time.
func (GrokRoutinePlugin) SecretHeader() (string, string) { return "Authorization", "Bearer " }

// ValidateSecret accepts one raw sender key: printable ASCII with no
// whitespace, never a pre-built header value or a key=value line. The value is
// never echoed.
func (GrokRoutinePlugin) ValidateSecret(secret string) error {
	if strings.HasPrefix(strings.ToLower(secret), "bearer ") || strings.HasPrefix(strings.ToLower(secret), "authorization:") {
		return &Error{Code: CodeTargetSecretInvalid, Message: "store only the raw routine sender key; PAIMOS adds the Authorization: Bearer header itself"}
	}
	if len(secret) < 8 || len(secret) > 512 {
		return &Error{Code: CodeTargetSecretInvalid, Message: "routine sender key must be 8 to 512 bytes"}
	}
	for _, r := range secret {
		if r < '!' || r > '~' {
			return &Error{Code: CodeTargetSecretInvalid, Message: "routine sender key must be printable ASCII without whitespace"}
		}
	}
	return nil
}

func WebhookHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, allowed := range strings.Split(os.Getenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS"), ",") {
		if strings.ToLower(strings.TrimSuffix(strings.TrimSpace(allowed), ".")) == host && host != "" {
			return true
		}
	}
	return false
}

func WebhookIPAllowed(ip net.IP) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS")), "true") {
		return true
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast())
}

func init() {
	if err := Register(GrokRoutinePlugin{}); err != nil {
		panic(err)
	}
}
