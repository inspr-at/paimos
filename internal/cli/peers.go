// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/inbox"
)

func (rt *runtime) cmdPeers() *Command {
	var project, after string
	limit := 100
	return &Command{Name: "peers", Short: "Find your live agent sessions across projects", Use: "peers [--project KEY] [--limit 1..100] [--after SESSION-UUID]", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 'p', "filter by recipient project")
			fs.string(&after, "after", 0, "continue after this session UUID")
			fs.int(&limit, "limit", "page size (1–100)")
		}, run: func([]string) error {
			if limit < 1 || limit > 100 || after != "" && !validUUID(after) {
				return usagef("invalid --limit or --after")
			}
			q := url.Values{"limit": {fmt.Sprint(limit)}}
			if after != "" {
				q.Set("after", after)
			}
			if err := rt.peerProjectFilter(q, project); err != nil {
				return err
			}
			var page inbox.PeerPage
			if err := rt.do(http.MethodGet, "/api/peers?"+q.Encode(), nil, &page); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(page)
			}
			if len(page.Items) == 0 {
				_, err := fmt.Fprintln(rt.stdout, "(no live peers)")
				return err
			}
			w := tabwriter.NewWriter(rt.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPROJECT\tHARNESS\tSTATE\tADDRESS")
			for _, item := range page.Items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", item.Name, item.Project, item.Harness, item.State, item.Address)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if page.NextAfter != nil {
				_, err := fmt.Fprintf(rt.stdout, "more peers: use --after %s\n", *page.NextAfter)
				return err
			}
			return nil
		}}
}

func (rt *runtime) peerProjectFilter(q url.Values, project string) error {
	if strings.TrimSpace(project) == "" {
		return nil
	}
	id, err := rt.projectIdentityCtx(rt.context(), project)
	if err != nil {
		return err
	}
	q.Set("project", id)
	return nil
}

func peerTellAddress(address string) bool {
	return strings.HasPrefix(address, "session:") || !validUUID(address) && !strings.Contains(address, ":")
}

func (rt *runtime) resolvePeer(address, project, session string) (inbox.Peer, error) {
	q := url.Values{"limit": {"2"}}
	if strings.HasPrefix(address, "session:") {
		id := strings.TrimPrefix(address, "session:")
		if !validUUID(id) || session != "" && !strings.EqualFold(id, session) {
			return inbox.Peer{}, usagef("invalid or conflicting peer session address")
		}
		q.Set("session", id)
	} else {
		if !utf8.ValidString(address) || len(address) > 640 || utf8.RuneCountInString(address) > 160 || strings.ContainsAny(address, "\x00\r\n") {
			return inbox.Peer{}, usagef("invalid peer name")
		}
		q.Set("name", address)
		if session != "" {
			q.Set("session", session)
		}
	}
	if err := rt.peerProjectFilter(q, project); err != nil {
		return inbox.Peer{}, err
	}
	var page inbox.PeerPage
	if err := rt.do(http.MethodGet, "/api/peers?"+q.Encode(), nil, &page); err != nil {
		return inbox.Peer{}, err
	}
	if len(page.Items) == 0 {
		return inbox.Peer{}, fmt.Errorf("no live owned peer %q on the selected instance; use aeon peers", address)
	}
	if len(page.Items) != 1 || page.NextAfter != nil {
		return inbox.Peer{}, fmt.Errorf("peer name %q is ambiguous; use an exact session:UUID address from aeon peers", address)
	}
	peer := page.Items[0]
	if !validUUID(peer.SessionID) || !validUUID(peer.PrincipalID) || !validUUID(peer.ProjectID) {
		return inbox.Peer{}, fmt.Errorf("invalid peer directory identity")
	}
	return peer, nil
}

// Keep the exact configured generation, including when it belongs to another
// project. The peer endpoint checks ownership and project authority at commit.
// No configured binding falls through to a newer or different session.
func (rt *runtime) ambientPeerSenderSession() (string, error) {
	id, err := inboxHookSession()
	if err != nil {
		return "", fmt.Errorf("peer sender binding unavailable; supply --sender-session: %w", err)
	}
	if id == "" && !hookSessionEnvSet() {
		if ref := ambientVendorSessionRef(); ref != "" {
			return rt.lookupVendorSession(rt.context(), ref)
		}
	}
	return id, nil
}
