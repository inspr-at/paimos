// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

type pendingCall struct {
	ch     chan Response
	closed bool
}

// Client owns stdin/stdout JSONL transport for one owned Pi child. Correlation,
// deadlines, and unsolicited/late response handling stay here; generation and
// lease authority remain at the agentd supervisor boundary.
type Client struct {
	writer io.Writer
	events chan PublicEvent

	writeMu sync.Mutex
	rpcMu   sync.Mutex
	pending map[string]*pendingCall

	streamDone chan struct{}
	streamOnce sync.Once

	onProtocolFault func()
}

func NewClient(stdout io.Reader, stdin io.Writer) *Client {
	c := &Client{
		writer:    stdin,
		events:    make(chan PublicEvent, 64),
		pending:   make(map[string]*pendingCall),
		streamDone: make(chan struct{}),
	}
	go c.readLoop(stdout)
	return c
}

func (c *Client) SetProtocolFaultHandler(fn func()) {
	c.onProtocolFault = fn
}

func (c *Client) Events() <-chan PublicEvent {
	return c.events
}

func (c *Client) StreamDone() <-chan struct{} {
	return c.streamDone
}

func (c *Client) readLoop(reader io.Reader) {
	defer c.streamOnce.Do(func() { close(c.streamDone) })
	r := NewReader(reader)
	for {
		line, err := r.ReadLine()
		if err == io.EOF {
			c.failPending(ErrStreamEnded)
			return
		}
		if err != nil {
			c.emitProtocolFault()
			c.failPending(err)
			return
		}
		if len(line) == 0 {
			continue
		}
		frame := ClassifyFrame(line)
		switch frame.Kind {
		case FrameInvalid:
			c.emitProtocolFault()
			c.failPending(ErrMalformedFrame)
			return
		case FrameResponse:
			c.deliverResponse(line)
		case FrameEvent:
			c.emitEvent(line)
		}
	}
}

func (c *Client) emitProtocolFault() {
	if c.onProtocolFault != nil {
		c.onProtocolFault()
	}
}

func (c *Client) emitEvent(raw json.RawMessage) {
	select {
	case c.events <- SanitizeEvent(raw):
	default:
	}
}

func (c *Client) deliverResponse(raw json.RawMessage) {
	var response Response
	if json.Unmarshal(raw, &response) != nil {
		c.emitProtocolFault()
		c.failPending(ErrMalformedFrame)
		return
	}
	id := response.ID
	if id == "" {
		// Unsolicited correlated response without id is ignored.
		return
	}
	c.rpcMu.Lock()
	call := c.pending[id]
	if call == nil || call.closed {
		c.rpcMu.Unlock()
		return
	}
	call.closed = true
	delete(c.pending, id)
	c.rpcMu.Unlock()
	select {
	case call.ch <- response:
	default:
	}
}

func (c *Client) failPending(err error) {
	c.rpcMu.Lock()
	pending := c.pending
	c.pending = make(map[string]*pendingCall)
	c.rpcMu.Unlock()
	for _, call := range pending {
		if !call.closed {
			call.closed = true
			select {
			case call.ch <- Response{Success: false, Error: err.Error()}:
			default:
			}
		}
	}
}

// Call sends one correlated command and waits for its response or deadline.
func (c *Client) Call(ctx context.Context, correlationID string, command Command) (Response, error) {
	if correlationID == "" {
		return Response{}, ErrInvalidCorrelation
	}
	command.ID = correlationID
	command.Type = normalizeCommandType(command.Type)

	c.rpcMu.Lock()
	if existing := c.pending[correlationID]; existing != nil && !existing.closed {
		c.rpcMu.Unlock()
		return Response{}, ErrDuplicateResponse
	}
	call := &pendingCall{ch: make(chan Response, 1)}
	c.pending[correlationID] = call
	c.rpcMu.Unlock()

	defer func() {
		c.rpcMu.Lock()
		if pending := c.pending[correlationID]; pending == call {
			delete(c.pending, correlationID)
		}
		c.rpcMu.Unlock()
	}()

	if err := c.write(command); err != nil {
		return Response{}, err
	}

	select {
	case response := <-call.ch:
		if !response.Success && response.Error == "" && response.Command == "" {
			return response, ErrCommandRejected
		}
		if !response.Success {
			if response.Error != "" {
				return response, errors.New(response.Error)
			}
			return response, ErrCommandRejected
		}
		return response, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-c.streamDone:
		return Response{}, ErrStreamEnded
	}
}

func (c *Client) write(command Command) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writer == nil {
		return ErrProcessUnavailable
	}
	return WriteLine(c.writer, command)
}

func (c *Client) closeWriter() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.writer = nil
}

func normalizeCommandType(commandType string) string {
	switch commandType {
	case "follow_up":
		return "follow_up"
	case "clear_queue":
		return "clear_queue"
	case "get_state":
		return "get_state"
	default:
		return commandType
	}
}

func acceptanceFrom(correlationID string, response Response, err error) Acceptance {
	acc := Acceptance{CorrelationID: correlationID, Command: response.Command, Accepted: err == nil && response.Success}
	if err != nil {
		acc.Error = RedactDiagnostic(err)
	} else if !response.Success {
		acc.Error = response.Error
	}
	return acc
}
