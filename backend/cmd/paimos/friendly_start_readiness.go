// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	friendlyReady       = "ready"
	friendlyMissing     = "missing"
	friendlyUnavailable = "unavailable"
	friendlyStale       = "stale"
	friendlyUnknown     = "unknown"
)

var friendlyAddressPart = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type friendlyReadinessLayer struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

type friendlyMessagingReadiness struct {
	SenderAddress   string                 `json:"sender_address,omitempty"`
	ReceiverAddress string                 `json:"receiver_address,omitempty"`
	Generation      friendlyReadinessLayer `json:"generation"`
	Sender          friendlyReadinessLayer `json:"sender_registration"`
	Policy          friendlyReadinessLayer `json:"receiver_policy"`
	Fallback        friendlyReadinessLayer `json:"optional_fallback"`
}

type friendlyTargetRow struct {
	Address, Adapter, Role string
	Enabled                bool
}

func friendlyApplyMessaging(ctx context.Context, client *Client, o friendlyStartOptions, result friendlyStartResult, public models.HarnessSession, session agentd.Session, cached bool) friendlyStartResult {
	if result.Plan == nil {
		return result
	}
	receiver := strings.TrimSpace(session.Identity)
	if receiver == "" {
		receiver = strings.TrimSpace(result.Plan.Profile.Harness + ":" + result.Plan.Agent)
	}
	projectID := public.ProjectID
	if projectID <= 0 && result.Plan.request.ProjectID > 0 {
		projectID = result.Plan.request.ProjectID
	}
	if projectID <= 0 {
		if project, err := resolveExactOrchestratorProject(client, o.Project); err == nil {
			projectID = project.ID
		}
	}
	if public.ID == "" {
		public.ID = result.PublicSessionID
	}
	if public.ProjectID == 0 {
		public.ProjectID = projectID
	}
	previous := result.Readiness
	readiness := probeFriendlyMessaging(ctx, client, projectID, receiver, previous, cached)
	readiness.Generation = friendlyGenerationLayer(result)
	result.Readiness = &readiness
	if result.Commands == nil {
		result.Commands = map[string]string{}
	}
	if cached {
		result.Commands["receiver-setup"] = friendlyCLIBase(client) + " runtime handoff --project " + shellQuote(o.Project)
		delete(result.Commands, "allow")
	}
	if result.Outcome == "started" {
		prefix := "Public registration verified. Generation state is the observed daemon state, not proof of task completion."
		if strings.Contains(result.Reason, "reconciled") {
			prefix = "Original daemon generation reconciled and public registration verified; no spawn was repeated."
		}
		result.Reason = friendlyStartedReason(prefix, readiness)
		if allow := friendlyAllowCommand(client, o, readiness); allow != "" {
			result.Commands["allow"] = allow
		}
	}
	return result
}

func friendlyGenerationLayer(result friendlyStartResult) friendlyReadinessLayer {
	if result.Outcome == "started" && result.PublicSessionID != "" {
		return friendlyReadinessLayer{State: friendlyReady, Code: "public_registration_verified"}
	}
	if result.Outcome == "failed" {
		return friendlyReadinessLayer{State: friendlyMissing, Code: "generation_failed"}
	}
	return friendlyReadinessLayer{State: friendlyUnknown, Code: "generation_unverified"}
}

func probeFriendlyMessaging(ctx context.Context, client *Client, projectID int64, receiver string, previous *friendlyMessagingReadiness, cached bool) friendlyMessagingReadiness {
	out := friendlyMessagingReadiness{ReceiverAddress: receiver}
	if !friendlyExactAddress(receiver) {
		out.ReceiverAddress = ""
		out.Sender = friendlyReadinessLayer{State: friendlyUnknown, Code: "receiver_identity_unverified"}
		out.Policy = staleOrUnavailable(layerOf(previous, "policy"), cached, "receiver_identity_unverified")
		out.Fallback = staleOrUnavailable(layerOf(previous, "fallback"), cached, "receiver_identity_unverified")
		return out
	}
	name, address, senderLayer := friendlyAttributedSender()
	out.SenderAddress = address
	out.Sender = senderLayer
	if projectID <= 0 {
		out.Sender = staleOrUnavailable(layerOf(previous, "sender"), cached, "project_scope_unverified")
		if senderLayer.State == friendlyUnknown {
			out.Sender = senderLayer
		}
		out.Policy = staleOrUnavailable(layerOf(previous, "policy"), cached, "project_scope_unverified")
		out.Fallback = staleOrUnavailable(layerOf(previous, "fallback"), cached, "project_scope_unverified")
		return out
	}
	if senderLayer.State == "" {
		out.Sender = friendlySenderRegistration(ctx, client, projectID, name, address, cached, previous)
	}
	out.Policy = friendlyReceiverPolicy(ctx, client, projectID, receiver, out.SenderAddress, out.Sender, cached, previous)
	out.Fallback = friendlyFallbackLayer(ctx, client, projectID, receiver, cached, previous)
	return out
}

func friendlyAttributedSender() (name, address string, layer friendlyReadinessLayer) {
	agent, _ := resolveAgentAttribution()
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return "", "", friendlyReadinessLayer{State: friendlyUnknown, Code: "attributed_sender_unknown"}
	}
	if i := strings.IndexByte(agent, ':'); i >= 0 {
		address := strings.ToLower(agent[:i]) + agent[i:]
		if !friendlyExactAddress(address) {
			return "", "", friendlyReadinessLayer{State: friendlyUnknown, Code: "attributed_sender_unknown"}
		}
		return agent, address, friendlyReadinessLayer{}
	}
	if !orchestratorAgentKeyPattern.MatchString(agent) || agent == "web-ui" {
		return "", "", friendlyReadinessLayer{State: friendlyUnknown, Code: "attributed_sender_unknown"}
	}
	return agent, "paimos:" + agent, friendlyReadinessLayer{}
}

func friendlyExactAddress(raw string) bool {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	return len(parts) == 2 && friendlyAddressPart.MatchString(parts[0]) && orchestratorAgentKeyPattern.MatchString(parts[1]) && parts[1] != "web-ui"
}

func friendlySenderRegistration(ctx context.Context, client *Client, projectID int64, name, address string, cached bool, previous *friendlyMessagingReadiness) friendlyReadinessLayer {
	var rows []struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"project_id"`
		Name      string `json:"name"`
	}
	if err := client.getJSON(ctx, fmt.Sprintf("/api/projects/%d/agents", projectID), &rows); err != nil {
		return staleOrUnavailable(layerOf(previous, "sender"), cached, "sender_registry_unavailable")
	}
	found := false
	for _, row := range rows {
		if row.ID <= 0 || row.ProjectID != projectID || !orchestratorAgentKeyPattern.MatchString(row.Name) {
			return staleOrUnavailable(layerOf(previous, "sender"), cached, "sender_registry_unverified")
		}
		if row.Name == name {
			found = true
		}
	}
	if !found {
		return friendlyReadinessLayer{State: friendlyMissing, Code: "sender_unregistered"}
	}
	if address == "" {
		return friendlyReadinessLayer{State: friendlyUnknown, Code: "attributed_sender_unknown"}
	}
	return friendlyReadinessLayer{State: friendlyReady, Code: "sender_registered"}
}

func friendlyReceiverPolicy(ctx context.Context, client *Client, projectID int64, receiver, sender string, senderLayer friendlyReadinessLayer, cached bool, previous *friendlyMessagingReadiness) friendlyReadinessLayer {
	if senderLayer.State == friendlyUnknown || sender == "" || !friendlyExactAddress(sender) {
		return friendlyReadinessLayer{State: friendlyUnknown, Code: "allowlist_sender_unknown"}
	}
	if senderLayer.State == friendlyMissing {
		return friendlyReadinessLayer{State: friendlyMissing, Code: "allowlist_sender_unregistered"}
	}
	if senderLayer.State != friendlyReady {
		return staleOrUnavailable(layerOf(previous, "policy"), cached, "allowlist_sender_unverified")
	}
	path := fmt.Sprintf("/api/projects/%d/message-allowlist?receiver=%s", projectID, url.QueryEscape(receiver))
	var raw json.RawMessage
	if err := client.getJSON(ctx, path, &raw); err != nil {
		return staleOrUnavailable(layerOf(previous, "policy"), cached, "allowlist_metadata_unavailable")
	}
	var body struct {
		Receiver string    `json:"receiver"`
		Senders  *[]string `json:"senders"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Senders == nil || body.Receiver != "" && body.Receiver != receiver {
		return staleOrUnavailable(layerOf(previous, "policy"), cached, "allowlist_metadata_unverified")
	}
	for _, granted := range *body.Senders {
		if granted == "*" || granted == "" || !friendlyExactAddress(granted) {
			return friendlyReadinessLayer{State: friendlyUnknown, Code: "allowlist_not_exact"}
		}
		if granted == sender {
			return friendlyReadinessLayer{State: friendlyReady, Code: "exact_sender_granted"}
		}
	}
	return friendlyReadinessLayer{State: friendlyMissing, Code: "exact_sender_not_granted"}
}

func friendlyFallbackLayer(ctx context.Context, client *Client, projectID int64, receiver string, cached bool, previous *friendlyMessagingReadiness) friendlyReadinessLayer {
	free, ok := friendlyReceiverSlot(ctx, client, projectID, receiver)
	if !ok {
		return staleOrUnavailable(layerOf(previous, "fallback"), cached, "fallback_metadata_unavailable")
	}
	if free {
		return friendlyReadinessLayer{State: friendlyMissing, Code: "optional_fallback_unconfigured"}
	}
	return friendlyReadinessLayer{State: friendlyReady, Code: "existing_or_primary_target_observed"}
}

func friendlyReceiverSlot(ctx context.Context, client *Client, projectID int64, identity string) (free bool, ok bool) {
	if projectID <= 0 || identity == "" {
		return false, false
	}
	var inventory struct {
		Targets []friendlyTargetRow `json:"targets"`
	}
	if friendlyRead(ctx, client, fmt.Sprintf("/api/projects/%d/message-targets?address=%s", projectID, url.QueryEscape(identity)), &inventory) != nil {
		return false, false
	}
	free = true
	for _, target := range inventory.Targets {
		if target.Address != identity || target.Enabled && (target.Role == "simple_fallback" || target.Role == "primary" && target.Adapter != "managed_harness") {
			free = false
		}
	}
	return free, true
}

func layerOf(previous *friendlyMessagingReadiness, name string) *friendlyReadinessLayer {
	if previous == nil {
		return nil
	}
	switch name {
	case "sender":
		return &previous.Sender
	case "policy":
		return &previous.Policy
	case "fallback":
		return &previous.Fallback
	}
	return nil
}

func staleOrUnavailable(previous *friendlyReadinessLayer, cached bool, code string) friendlyReadinessLayer {
	if cached && previous != nil && previous.State == friendlyReady {
		return friendlyReadinessLayer{State: friendlyStale, Code: code}
	}
	return friendlyReadinessLayer{State: friendlyUnavailable, Code: code}
}

func friendlyStartedReason(prefix string, r friendlyMessagingReadiness) string {
	reason := prefix
	switch r.Sender.State {
	case friendlyReady:
		reason += " Attributed sender " + r.SenderAddress + " is registered."
	case friendlyMissing:
		reason += " Attributed sender " + r.SenderAddress + " is not registered in this project."
	case friendlyUnknown:
		reason += " Attributed sender is unknown and was not inferred from the parent or a running process."
	default:
		reason += " Attributed sender registration evidence is " + r.Sender.State + "."
	}
	switch r.Policy.State {
	case friendlyReady:
		reason += " Exact receiver allowlist grants " + r.SenderAddress + " -> " + r.ReceiverAddress + "."
	case friendlyMissing:
		if r.Sender.State == friendlyMissing {
			reason += " Receiver allowlist grant cannot be issued until that sender is registered."
		} else {
			reason += " Exact receiver allowlist does not grant " + r.SenderAddress + " -> " + r.ReceiverAddress + "."
		}
	case friendlyUnknown:
		reason += " Receiver allowlist grant is unknown without an authoritative sender."
	default:
		reason += " Receiver allowlist metadata is " + r.Policy.State + "; ordinary delivery is unverified."
	}
	switch r.Fallback.State {
	case friendlyMissing:
		reason += " Optional simple fallback and root attention require the reviewed receiver-setup action."
	case friendlyReady:
		reason += " Optional fallback remains distinct from primary sender policy; existing targets were not replaced."
	default:
		reason += " Optional fallback target evidence is " + r.Fallback.State + "; do not replace an existing target."
	}
	reason += " Action-request messages stay held for human review; start does not resolve, dismiss, or release them."
	if r.Sender.State != friendlyReady || r.Policy.State != friendlyReady {
		reason += " A started generation is not proof that this sender can already deliver ordinary messages."
	} else {
		reason += " Ordinary tell from this exact sender may be delivered to this receiver's primary inbox."
	}
	return reason
}

func friendlyAllowCommand(client *Client, o friendlyStartOptions, r friendlyMessagingReadiness) string {
	if r.Sender.State != friendlyReady || r.Policy.State != friendlyMissing || !friendlyExactAddress(r.SenderAddress) || !friendlyExactAddress(r.ReceiverAddress) {
		return ""
	}
	base := "paimos"
	if flagConfigPath != "" {
		base += " --config " + shellQuote(flagConfigPath)
	}
	if client != nil && client.identity.Name != "" && client.identity.Name != "env" {
		base += " --instance " + shellQuote(client.identity.Name)
	}
	return base + " message allow " + shellQuote(r.SenderAddress) + " --project " + shellQuote(o.Project) + " --for " + shellQuote(r.ReceiverAddress)
}
