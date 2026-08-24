package handlers

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
)

func TestAgentMessageCodedErrorUsesStableProblemCode(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/projects/1/messages", nil)
	writeAgentMessageError(recorder, request, &agentmessage.CodedError{
		Code: "agent_message_addressee_unknown", Err: errors.New("addressee is not registered in this project"),
	})
	var problem ProblemDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "agent_message_addressee_unknown" || problem.Status != 400 {
		t.Fatalf("problem=%#v", problem)
	}
}
