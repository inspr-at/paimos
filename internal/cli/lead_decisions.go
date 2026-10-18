// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) cmdProjectDecisions() *Command {
	var after, session string
	limit := 50
	return &Command{Name: "decisions", Short: "Replay redacted lead decisions; evidence grants no authority", Use: "project decisions <key|id> [--after EVENT-ID] [--limit 1..200] [--session UUID]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&after, "after", 0, "exclusive tenant-local event ID cursor")
			fs.int(&limit, "limit", "maximum decisions (1..200)")
			fs.string(&session, "session", 0, "filter by coordinator generation UUID")
		}, run: func(args []string) error {
			cursor := int64(0)
			var err error
			if after != "" {
				cursor, err = strconv.ParseInt(after, 10, 64)
			}
			if err != nil || cursor < 0 || limit < 1 || limit > 200 || session != "" && !validUUID(session) {
				return usagef("invalid decision cursor, limit or session")
			}
			id, err := rt.decisionProjectID(args[0])
			if err != nil {
				return err
			}
			query := url.Values{"after": {strconv.FormatInt(cursor, 10)}, "limit": {strconv.Itoa(limit)}}
			if session != "" {
				query.Set("session_id", session)
			}
			var out harness.LeadDecisionPage
			if err = rt.do(http.MethodGet, "/api/projects/"+url.PathEscape(id)+"/lead-decisions?"+query.Encode(), nil, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			for _, item := range out.Items {
				if _, err = fmt.Fprintf(rt.stdout, "#%d %s %s · %s · policy %s/%d · attempt %d · reported evidence\n", item.EventID, item.Request.Stage, item.Outcome, strings.Join(item.ReasonCodes, ", "), item.Request.PolicySource, item.Request.PolicyRevision, item.Request.Attempt); err != nil {
					return err
				}
			}
			if len(out.Items) == 0 {
				if _, err = fmt.Fprintln(rt.stdout, "No recorded lead decisions"); err != nil {
					return err
				}
			}
			if out.NextAfter != nil {
				_, err = fmt.Fprintf(rt.stdout, "More decisions: --after %d\n", *out.NextAfter)
			}
			return err
		}}
}

func (rt *runtime) harnessDecision() *Command {
	var project, session, bodyFile, leaseFile string
	return &Command{Name: "decision", Short: "Record prompt-free lead evidence without executing work", Use: "harness decision --project KEY --session UUID --body-file PATH --worker-lease-file PATH", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 'p', "project key or UUID")
			fs.string(&session, "session", 0, "owning coordinator generation UUID")
			fs.string(&bodyFile, "body-file", 0, "typed decision JSON file, or - for stdin (at most 8192 bytes)")
			fs.string(&leaseFile, "worker-lease-file", 0, "private generation lease file")
		}, run: func([]string) error {
			if !validUUID(session) || bodyFile == "" || project == "" || leaseFile == "" || bodyFile == "-" && leaseFile == "-" {
				return usagef("project, session, body-file and separate worker-lease-file are required")
			}
			var raw []byte
			var err error
			if bodyFile == "-" {
				raw, err = readBounded(rt.stdin, 8192)
			} else {
				raw, err = readBoundedFile(bodyFile, 8192)
			}
			if err != nil {
				return err
			}
			if len(raw) > 8192 {
				return usagef("decision evidence exceeds 8192 bytes")
			}
			var in harness.LeadDecisionWrite
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&in); err != nil {
				return usagef("invalid decision JSON")
			}
			var extra any
			if err = decoder.Decode(&extra); err != io.EOF {
				return usagef("expected one decision JSON object")
			}
			in, err = harness.NormalizeLeadDecision(in)
			if err != nil {
				return usagef("invalid decision evidence")
			}
			id, err := rt.decisionProjectID(project)
			if err != nil {
				return err
			}
			lease, err := rt.harnessSecret(leaseFile, "worker-lease-file")
			if err != nil {
				return err
			}
			if len(lease) < 32 {
				return usagef("worker lease must contain at least 32 characters")
			}
			var out harness.LeadDecisionRecorded
			if err = rt.harnessDo(http.MethodPost, harnessPath(id, session)+"/lead-decisions", lease, in, &out); err != nil {
				return err
			}
			return rt.printJSON(out)
		}}
}

func (rt *runtime) decisionProjectID(ref string) (string, error) {
	if validUUID(ref) {
		return strings.ToLower(ref), nil
	}
	return rt.harnessProject(ref)
}
