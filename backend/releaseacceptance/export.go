// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

func (s *Service) Export(ctx context.Context, actor Actor, projectID, releaseID int64, format string) (string, []byte, error) {
	acc, err := s.Get(ctx, actor, projectID, releaseID)
	if err != nil {
		return "", nil, err
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "json":
		body, err := json.MarshalIndent(acc, "", "  ")
		return "application/json", body, err
	case "eml":
		raw, err := s.rawMessages(ctx, projectID, releaseID)
		if err != nil {
			return "", nil, err
		}
		return "message/rfc822", raw, nil
	case "html":
		return "text/html; charset=utf-8", []byte(printableHTML(acc)), nil
	default:
		return "", nil, fmt.Errorf("%w: format", ErrInvalid)
	}
}

func (s *Service) rawMessages(ctx context.Context, projectID, releaseID int64) ([]byte, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT e.raw_message, e.state, COALESCE(o.state,''), COALESCE(o.last_error_class,'')
		FROM acceptance_email_evidence e
		JOIN release_records r ON r.id=e.release_id
		LEFT JOIN acceptance_mail_outbox o ON o.evidence_id=e.id
		WHERE r.project_id=? AND r.id=? AND e.raw_message IS NOT NULL
		ORDER BY e.id`, projectID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parts [][]byte
	for rows.Next() {
		var raw []byte
		var state, outbox, class string
		if err := rows.Scan(&raw, &state, &outbox, &class); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		marker := []byte("X-Paimos-Delivery-State: " + displayMailState(state, outbox, class) + "\r\n")
		parts = append(parts, append(marker, raw...))
	}
	if len(parts) == 0 {
		return []byte("Subject: (no sent email evidence)\r\n\r\n"), nil
	}
	return bytesJoin(parts, []byte("\r\n\r\n")), rows.Err()
}

func bytesJoin(parts [][]byte, sep []byte) []byte {
	if len(parts) == 0 {
		return nil
	}
	n := len(sep) * (len(parts) - 1)
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			out = append(out, sep...)
		}
		out = append(out, p...)
	}
	return out
}

func printableHTML(acc Acceptance) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(acc.Release.ReleaseRef))
	b.WriteString("</title></head><body>")
	b.WriteString("<h1>Release acceptance</h1>")
	b.WriteString("<p>" + html.EscapeString(OfferDisclaimer) + "</p>")
	b.WriteString("<p>Operating arrangement: " + html.EscapeString(acc.OperatingModeLabel) + "</p>")
	b.WriteString("<p>Release: " + html.EscapeString(acc.Release.ReleaseRef) + " · artifact " + html.EscapeString(acc.Release.ArtifactDigest) + "</p>")
	b.WriteString("<p>Status: " + html.EscapeString(acc.Status) + "</p>")
	b.WriteString("<p>Agreement: " + html.EscapeString(acc.AgreementRef) + "</p>")
	b.WriteString("<h2>Disclosed gaps</h2><ul>")
	if len(acc.DisclosedGaps) == 0 {
		b.WriteString("<li>None disclosed. Gaps listed here are not a universal legal checklist.</li>")
	}
	for _, g := range acc.DisclosedGaps {
		b.WriteString("<li>" + html.EscapeString(g.Statement) + "</li>")
	}
	b.WriteString("</ul><h2>Parties</h2><ul>")
	for _, p := range acc.Parties {
		label := p.DisplayName
		if label == "" {
			label = p.Email
		}
		b.WriteString("<li>" + html.EscapeString(label) + "</li>")
	}
	b.WriteString("</ul><h2>Confirmations</h2><ul>")
	for _, c := range acc.Confirmations {
		name := c.PartyName
		if name == "" {
			name = partyDisplayName(acc, c.PartyRef)
		}
		b.WriteString("<li>" + html.EscapeString(name) + " · " + html.EscapeString(c.SourceLabel) + " · revision " + html.EscapeString(fmt.Sprintf("%d", c.AcceptanceRevision)) + " · " + html.EscapeString(c.ConfirmedAt) + "</li>")
	}
	b.WriteString("</ul><h2>Email evidence</h2><ul>")
	for _, e := range acc.EmailEvidence {
		state := e.DisplayState
		if state == "" {
			state = e.State
		}
		b.WriteString("<li>" + html.EscapeString(state) + " · " + html.EscapeString(strings.Join(e.RecipientNames, ", ")) + " · " + html.EscapeString(e.RecordedAt) + "</li>")
	}
	b.WriteString("</ul><h2>Missing</h2><p>Confirmations: " + html.EscapeString(strings.Join(namedPartyList(acc, acc.Missing.Confirmations), ", ")) +
		"<br>Email coverage: " + html.EscapeString(strings.Join(namedPartyList(acc, acc.Missing.EmailCoverage), ", ")) + "</p>")
	b.WriteString("<h2>Reviewable message</h2><pre>")
	b.WriteString(html.EscapeString(acc.PreviewSubject + "\n\n" + acc.PreviewBody))
	b.WriteString("</pre></body></html>")
	return b.String()
}

func namedPartyList(acc Acceptance, refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, partyDisplayName(acc, ref))
	}
	return out
}
