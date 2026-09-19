// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import "encoding/json"

func (p *codexProcess) handleNativeMessage(message codexRPCMessage) {
	reply := func(receipt NativeMessageReceipt) {
		body, _ := json.Marshal(receipt)
		_ = p.send(map[string]any{"id": message.ID, "result": map[string]any{
			"success": receipt.Error == "", "contentItems": []map[string]string{{"type": "inputText", "text": string(body)}}}})
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
	p.stateMu.Lock()
	valid := params.ThreadID == p.threadID && params.TurnID == p.turnID && validOpaqueID(params.TurnID) && !p.terminalFailure
	p.stateMu.Unlock()
	if !valid {
		reply(NativeMessageReceipt{Error: "sender_unavailable"})
		return
	}
	p.nativeMessages.run(p.streamDone, params.CallID, params.Arguments, reply)
}
