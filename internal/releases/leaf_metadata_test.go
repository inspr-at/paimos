// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestLeafParentRoundTripPreservesPlanningMetadata(t *testing.T) {
	for _, source := range []string{"manual", "requirements"} {
		t.Run(source, func(t *testing.T) {
			f := ticketSetup(t)
			former := f.existing("work", f.feature, "Planned leaf", "open")
			membershipOK(t, f.addExisting([]string{former}, 1, false))
			var original json.RawMessage
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET feature_node_id=$2,source=$3,
 estimated_hours=7.25,access_change=true,scope_revision_required=false,walker_position=42 WHERE ticket_node_id=$1`, former, f.feature, source); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&original)
			})
			child := f.existing("work", former, "New child", "open")
			for round := 0; round < 2; round++ {
				f.tx(func(tx pgx.Tx) error {
					var live int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=$1`, former).Scan(&live); err != nil {
						return err
					}
					if live != 0 {
						t.Fatal("parent retained live membership")
					}
					if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child); err != nil {
						return err
					}
					var restored json.RawMessage
					if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&restored); err != nil {
						return err
					}
					if string(restored) != string(original) {
						t.Fatalf("round %d lost planning metadata: before=%s after=%s", round, original, restored)
					}
					var access bool
					if err := tx.QueryRow(t.Context(), `SELECT access_required FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&access); err != nil {
						return err
					}
					if !access {
						t.Fatal("restored access-changing leaf lost the release access gate")
					}
					if round == 0 {
						_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, child)
						return err
					}
					return nil
				})
			}
		})
	}
}
