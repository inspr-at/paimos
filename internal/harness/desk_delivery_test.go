// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestDeskAnswerReferencesSurviveVerifiedResumeAndLateCorrection(t *testing.T) {
	f := fixture(t)
	path, lease, control := pauseSession(t, f, pauseRegistration(f))
	finishPaused(t, f, path, lease, control)
	id := path[strings.LastIndex(path, "/")+1:]
	question, answer, corrected := uid(), uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET desk_answers=jsonb_build_array(jsonb_build_object('question_id',$2::text,'answer_id',$3::text,'revision',2)) WHERE id=$1`, id, question, answer)
		return err
	})
	w := f.call(f.person, "POST", path+"/resume", map[string]any{"registration": map[string]any{"harness_session_ref": "desk-next-ref-" + uid(), "worker_lease": "desk-next-lease-" + uid()}}, "")
	expect(t, w, 200)
	next := decode(t, w)["successor"].(map[string]any)
	continuation := next["continuation"].(map[string]any)
	if !strings.Contains(continuation["brief"].(string), answer) || !strings.Contains(continuation["brief"].(string), "ask status "+question) {
		t.Fatal("resume brief lost authorized answer reference")
	}
	nextID := next["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET desk_answers=jsonb_build_array(jsonb_build_object('question_id',$2::text,'answer_id',$3::text,'revision',3,'replaces',$4::text)) WHERE id=$1`, nextID, question, corrected, answer)
		return err
	})
	w = f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+nextID, nil, "")
	expect(t, w, 200)
	continuation = decode(t, w)["continuation"].(map[string]any)
	if !strings.Contains(continuation["brief"].(string), corrected) || !strings.Contains(continuation["brief"].(string), "replaces="+answer) {
		t.Fatal("late correction did not refresh successor brief")
	}
}
