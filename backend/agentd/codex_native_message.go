// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"encoding/json"
	"time"
)

func (p *codexProcess) handleNativeMessage(message codexRPCMessage) {
	if len(message.ID) > 256 {
		_, _ = p.signalOwned(true)
		return
	}
	requestID := append(json.RawMessage(nil), message.ID...)
	reply := func(receipt NativeMessageReceipt) {
		body, _ := json.Marshal(receipt)
		if !p.nativeReplies.enqueue(func() {
			_ = p.send(map[string]any{"id": requestID, "result": map[string]any{
				"success": receipt.Error == "", "contentItems": []map[string]string{{"type": "inputText", "text": string(body)}}}})
		}) {
			_, _ = p.signalOwned(true)
		}
	}
	var params struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		CallID    string          `json:"callId"`
		Tool      string          `json:"tool"`
		Namespace *string         `json:"namespace"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.Tool != NativeMessageTool || params.Namespace != nil {
		reply(NativeMessageReceipt{Error: "invalid_message"})
		return
	}
	p.nativeMessages.run(p.streamDone, params.CallID, params.Arguments, reply, func(ctx context.Context) bool {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if p.stateMu.TryLock() {
				valid := params.ThreadID == p.threadID && validOpaqueID(params.TurnID) && !p.terminalFailure
				starting := p.startingTurn
				owned := params.TurnID == p.turnID
				p.stateMu.Unlock()
				if !valid {
					return false
				}
				if !starting {
					return owned
				}
			}
			select {
			case <-ctx.Done():
				return false
			case <-ticker.C:
			}
		}
	})
}
