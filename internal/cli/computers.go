// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/inspr-at/paimos/internal/agentpairing"
)

func (rt *runtime) cmdComputers() *Command {
	var sessionFile, rawURL, cursor string
	limit := 50
	return &Command{Name: "computers", Short: "Paired computer evidence", Use: "computers inventory", subs: []*Command{{
		Name: "inventory", Short: "List ownership evidence without selecting an owner", Use: "computers inventory [--limit N] [--cursor UUID] [--session-file PATH] [--url URL]", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.int(&limit, "limit", "page size, 1–100")
			fs.string(&cursor, "cursor", 0, "next computer cursor")
			fs.string(&sessionFile, "session-file", 0, "person session cookie file, or - for stdin")
			fs.string(&rawURL, "url", 0, "instance URL for --session-file")
		},
		run: func([]string) error {
			if limit < 1 || limit > 100 {
				return usagef("limit must be between 1 and 100")
			}
			if cursor != "" && !validUUID(cursor) {
				return usagef("cursor must be a computer UUID")
			}
			query := url.Values{"limit": {strconv.Itoa(limit)}}
			if cursor != "" {
				query.Set("cursor", cursor)
			}
			var out agentpairing.ComputerInventory
			if err := rt.keyMetadataRequest("GET", "/api/agent-pairing/computers/inventory?"+query.Encode(), sessionFile, rawURL, nil, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			for _, v := range out.Computers {
				fmt.Fprintf(rt.stdout, "%s (%s) · %s · owner: %s\n  approved_by: %s\n", v.ComputerName, v.ComputerID, v.State, v.Owner, evidencePerson(v.ApprovedBy))
				for _, e := range v.WorkstationOwners {
					fmt.Fprintf(rt.stdout, "  workstation: person_id=%s issued_by=%s\n", evidencePerson(e.PersonID), evidencePerson(e.CreatedByPrincipalID))
				}
				for _, e := range v.Enrollments {
					fmt.Fprintf(rt.stdout, "  enrollment: %s · %s · owner_person_id=%s approved_by=%s\n", e.AccountID, e.State, evidencePerson(e.OwnerPersonID), evidencePerson(e.ApprovedBy))
				}
				if v.EnrollmentsTruncated {
					fmt.Fprintln(rt.stdout, "  enrollment evidence truncated after 100 rows")
				}
			}
			if out.NextCursor != nil {
				fmt.Fprintf(rt.stdout, "next_cursor: %s\n", *out.NextCursor)
			}
			return nil
		},
	}}}
}

func evidencePerson(id *string) string {
	if id == nil {
		return "unknown"
	}
	return *id
}
