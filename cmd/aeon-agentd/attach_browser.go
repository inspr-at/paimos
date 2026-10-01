// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/attachwatch"
)

// The reader is /dev/tty, opened and checked by attachCommand. There is no
// stdin, flag or remote request that can supply the local presence answer.
func confirmAttachApproval(ctx context.Context, reader *bufio.Reader, tty, out io.Writer, preview agentd.AttachLocalView, language string, manual bool, confirm func(context.Context, agentd.AttachLocalRequest) (agentd.AttachLocalView, error), opener func(context.Context, string) error) (agentd.AttachLocalView, error) {
	words := attachWording(language)
	next := words.Next
	if manual {
		next = words.NextManual
	}
	fmt.Fprintln(tty, next)
	if language == "de" {
		fmt.Fprintf(tty, "%s — Enter oder y zum Fortfahren, n zum Abbrechen: ", words.LocalCheck)
	} else {
		fmt.Fprintf(tty, "%s — press Enter or y to continue, n to cancel: ", words.LocalCheck)
	}
	answer, err := readAttachAnswer(ctx, reader)
	if err != nil {
		return agentd.AttachLocalView{}, err
	}
	if answer != "" && !strings.EqualFold(answer, "y") {
		return agentd.AttachLocalView{}, errors.New("attach cancelled")
	}
	view, err := confirm(ctx, agentd.AttachLocalRequest{Operation: "confirm", ID: preview.ID, Digest: preview.Digest})
	if err != nil {
		return agentd.AttachLocalView{}, err
	}
	fmt.Fprint(out, attachApprovalNotice(view.Origin, view.Code, view.ExpiresAt, time.Now()))
	openAttachApproval(ctx, out, view, language, manual, opener)
	return view, nil
}

// Build only an origin-pinned, fragment-only code link. No caller-supplied
// protocol, credentials, path, query, fragment or terminal controls reach open.
func attachApprovalURL(origin, code string) string {
	if !attachwatch.Text(origin, 2048) || agentd.ValidateBaseURL(origin) != nil || len(code) != 9 || strings.Trim(code, "0123456789") != "" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || u.Path != "" && u.Path != "/" || u.RawPath != "" || u.ForceQuery || strings.ContainsAny(origin, "\\\r\n\t") {
		return ""
	}
	u.Path = "/agents"
	u.Fragment = "attach=" + code
	return u.String()
}

// Called only after local confirmation and after printing the fallback. The
// system opener merely prefills a code; person approval and Touch ID stay in
// their existing paths. Opener errors never echo command arguments or output.
func openAttachApproval(ctx context.Context, out io.Writer, view agentd.AttachLocalView, language string, disabled bool, opener func(context.Context, string) error) {
	link := attachApprovalURL(view.Origin, view.Code)
	if disabled || view.State != "pending" || link == "" || view.ExpiresAt != nil && !time.Now().Before(*view.ExpiresAt) {
		return
	}
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if opener(openCtx, link) != nil {
		if language == "de" {
			fmt.Fprintln(out, "Der Browser konnte nicht geöffnet werden. Öffne den angezeigten Link und erteile die Freigabe in Aeon.")
		} else {
			fmt.Fprintln(out, "Could not open the browser. Open the printed link and approve in Aeon.")
		}
		return
	}
	if language == "de" {
		fmt.Fprintln(out, "Freigabe im Browser geöffnet. Prüfe diese Sitzung und erteile die Freigabe in Aeon; der Link genehmigt nichts.")
	} else {
		fmt.Fprintln(out, "Opened approval in your browser. Review this session and approve in Aeon; the link approves nothing.")
	}
}

func openAttachBrowser(ctx context.Context, link string) error {
	return exec.CommandContext(ctx, "/usr/bin/open", link).Run()
}
