// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"sync"
)

const (
	chatSubscriberLimit = 4
	chatQueueLimit      = 32
)

type ChatCapabilities struct {
	Steer     string `json:"steer"`
	Interrupt bool   `json:"interrupt"`
	Deltas    bool   `json:"deltas"`
}

// Capabilities describe the selected owned transport, never the model family
// alone. Exact per-session controls remain the authority for mutations.
func sessionChatCapabilities(harness string, caps []string) ChatCapabilities {
	result := ChatCapabilities{Steer: "queue"}
	if harness != Claude && harness != Codex && harness != Pi {
		return result
	}
	result.Deltas = true
	for _, cap := range caps {
		switch cap {
		case "interrupt":
			result.Interrupt = true
		case "steer":
			result.Steer = "native"
			if harness == Pi {
				result.Steer = "next-step"
			}
		}
	}
	return result
}

// ChatBinding contains public identifiers only; never expose the worker lease.
type ChatBinding struct {
	TenantID    string `json:"tenant_id"`
	PrincipalID string `json:"principal_id"`
	RunID       string `json:"run_id"`
	Generation  string `json:"generation"`
	SessionID   string `json:"session_id"`
}

type ChatSessionEvent struct {
	Binding       ChatBinding       `json:"binding"`
	Sequence      uint64            `json:"sequence"`
	DroppedEvents uint64            `json:"dropped_events"`
	Capabilities  *ChatCapabilities `json:"capabilities,omitempty"`
	Update        *ChatUpdate       `json:"update,omitempty"`
}

type chatSubscriber struct {
	events  chan ChatSessionEvent
	dropped uint64
}

// sessionChat never retains text for replay. Only content-free state survives
// publication, and subscriber queues are cleared on cancellation or exit.
type sessionChat struct {
	mu           sync.Mutex
	binding      ChatBinding
	capabilities ChatCapabilities
	state        string
	sequence     uint64
	closed       bool
	subscribers  map[*chatSubscriber]bool
	relay        *ChatRelay // optional server relay; its own bounded queue, never a lossy subscriber
}

func newSessionChat(binding ChatBinding, caps ChatCapabilities) *sessionChat {
	return &sessionChat{binding: binding, capabilities: caps, subscribers: map[*chatSubscriber]bool{}}
}

// SubscribeChat is a daemon-local seam for the authenticated chat relay. Public
// identifiers alone must not be used as authorization by any external handler.
// No HTTP route is added here. Cancellation releases queued transient content.
func (s *Supervisor) SubscribeChat(binding ChatBinding) (<-chan ChatSessionEvent, func(), error) {
	s.mu.Lock()
	entry := s.runs[binding.RunID]
	valid := binding.TenantID == s.tenantID && binding.PrincipalID == s.principalID
	s.mu.Unlock()
	if !valid || entry == nil {
		return nil, nil, ErrScope
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if binding.Generation != s.generation || binding.Generation != entry.record.Generation {
		return nil, nil, ErrGeneration
	}
	if binding.SessionID == "" || binding.SessionID != entry.harness.ID || entry.harnessArchived || entry.process == nil || entry.record.State != "running" || entry.chat == nil {
		return nil, nil, ErrNotOwned
	}
	return entry.chat.subscribe()
}

// attachRelay must precede the adapter start so the first turn is relayed.
func (c *sessionChat) attachRelay(r *ChatRelay) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.relay = r
}

func (c *sessionChat) subscribe() (<-chan ChatSessionEvent, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, ErrNotOwned
	}
	if len(c.subscribers) >= chatSubscriberLimit {
		return nil, nil, ErrUnsupported
	}
	sub := &chatSubscriber{events: make(chan ChatSessionEvent, chatQueueLimit)}
	caps := c.capabilities
	sub.events <- ChatSessionEvent{Binding: c.binding, Sequence: c.sequence, Capabilities: &caps}
	if c.state != "" {
		u := chatState(c.state)
		sub.events <- ChatSessionEvent{Binding: c.binding, Sequence: c.sequence, Update: &u}
	}
	c.subscribers[sub] = true
	return sub.events, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.subscribers[sub] {
			c.remove(sub)
		}
	}, nil
}

func (c *sessionChat) remove(sub *chatSubscriber) {
	delete(c.subscribers, sub)
	for {
		select {
		case <-sub.events:
		default:
			close(sub.events)
			return
		}
	}
}

func (c *sessionChat) publish(update ChatUpdate) {
	if !update.valid() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.sequence++
	if update.SessionUpdate == "state" {
		c.state = update.State
	}
	if c.relay != nil {
		relayed := update
		if update.Content != nil {
			content := *update.Content
			relayed.Content = &content
		}
		c.relay.Observe(ChatSessionEvent{Binding: c.binding, Sequence: c.sequence, Update: &relayed})
	}
	for sub := range c.subscribers {
		// Every subscriber owns its projection, including its text pointer.
		copy := update
		if update.Content != nil {
			content := *update.Content
			copy.Content = &content
		}
		ev := ChatSessionEvent{Binding: c.binding, Sequence: c.sequence, DroppedEvents: sub.dropped, Update: &copy}
		select {
		case sub.events <- ev:
		default:
			sub.dropped++
		}
	}
}

func (c *sessionChat) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for sub := range c.subscribers {
		c.remove(sub)
	}
	c.relay.Close()
}
