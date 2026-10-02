// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotedocument"
	"github.com/inspr-at/paimos/internal/plugins/fence"
)

func markdown(v version) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nQuote %s · version %d\n\nRecipient contact: %s\n\n", v.Title, v.QuoteNodeID, v.Version, v.RecipientContactNodeID)
	fmt.Fprintf(&b, "| Description | Quantity | Unit | Rate (%s) | Net (%s) | Tax |\n| --- | ---: | --- | ---: | ---: | ---: |\n", v.Currency, v.Currency)
	for _, l := range v.Lines {
		desc := strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(l.Description)
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", desc, l.Quantity, l.Unit, l.RateAmount, l.NetAmount, l.TaxRate)
	}
	fmt.Fprintf(&b, "\nSubtotal: %s %s  \nTax: %s %s  \nTotal: **%s %s**\n\n", v.Subtotal, v.Currency, v.TaxTotal, v.Currency, v.Total, v.Currency)
	if v.TermsMarkdown != "" {
		b.WriteString("## Terms\n\n")
		b.WriteString(v.TermsMarkdown)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\nOffer digest: `%s`\n", v.ContentSHA256)
	return b.String()
}

// pdf renders a compact, deterministic PDF from the immutable offer. Long
// Markdown text is preserved as plain lines across pages; no dynamic content
// or mutable export row is created.
func pdf(v version) []byte {
	lines := []string{v.Title, fmt.Sprintf("Quote %s / version %d", v.QuoteNodeID, v.Version), "Recipient contact: " + v.RecipientContactNodeID, ""}
	for _, l := range v.Lines {
		lines = append(lines, fmt.Sprintf("%s | %s %s x %s = %s %s; tax %s", l.Description, l.Quantity, l.Unit, l.RateAmount, l.NetAmount, v.Currency, l.TaxRate))
	}
	lines = append(lines, "", fmt.Sprintf("Subtotal %s %s", v.Subtotal, v.Currency), fmt.Sprintf("Tax %s %s", v.TaxTotal, v.Currency), fmt.Sprintf("Total %s %s", v.Total, v.Currency), "", "Terms:")
	lines = append(lines, strings.Split(v.TermsMarkdown, "\n")...)
	lines = append(lines, "", "SHA-256: "+v.ContentSHA256)
	wrapped := []string{}
	for _, s := range lines {
		runes := []rune(s)
		for len(runes) > 90 {
			cut := 90
			for cut > 40 && runes[cut] != ' ' {
				cut--
			}
			wrapped = append(wrapped, string(runes[:cut]))
			runes = []rune(strings.TrimLeft(string(runes[cut:]), " "))
		}
		wrapped = append(wrapped, string(runes))
	}
	var objects []string
	objects = append(objects, "") // catalog
	objects = append(objects, "") // pages
	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	pages := []int{}
	for i := 0; i < len(wrapped); i += 48 {
		end := i + 48
		if end > len(wrapped) {
			end = len(wrapped)
		}
		var content strings.Builder
		content.WriteString("BT /F1 10 Tf 48 754 Td 13 TL\n")
		for _, s := range wrapped[i:end] {
			content.WriteString("(")
			content.WriteString(pdfLiteral(s))
			content.WriteString(") Tj T*\n")
		}
		content.WriteString("ET\n")
		stream := content.String()
		contentID := len(objects) + 1
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
		pageID := len(objects) + 1
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentID))
		pages = append(pages, pageID)
	}
	var kids strings.Builder
	for _, p := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", p)
	}
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(pages))
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
func pdfLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r == '(' || r == ')' || r == '\\' {
			b.WriteByte('\\')
		}
		if r >= 32 && r <= 126 {
			b.WriteRune(r)
			continue
		}
		if r >= 160 && r <= 255 {
			fmt.Fprintf(&b, "\\%03o", r)
			continue
		}
		if r == '€' {
			b.WriteString("\\200")
			continue
		}
		if r == '–' {
			b.WriteString("\\226")
			continue
		}
		if r == '—' {
			b.WriteString("\\227")
			continue
		}
		if r == '‘' {
			b.WriteString("\\221")
			continue
		}
		if r == '’' {
			b.WriteString("\\222")
			continue
		}
		if r == '“' {
			b.WriteString("\\223")
			continue
		}
		if r == '”' {
			b.WriteString("\\224")
			continue
		}
		b.WriteByte('?')
	}
	return b.String()
}
func (m *Module) export(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	id, e := pathID(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	n, e := pathVersion(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	format := r.URL.Query().Get("format")
	if format != "markdown" && format != "pdf" {
		respond(w, 0, nil, bad("format must be markdown or pdf"))
		return
	}
	var v version
	e = m.tx(portalContext(r), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		q, err := readQuote(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		if err = canReadQuote(r.Context(), tx, p, q, n); err != nil {
			return err
		}
		v, err = readVersion(r.Context(), tx, id, n)
		return err
	})
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if quotedocument.IsDocument(v.DigestMode) {
		respond(w, 0, nil, conflict("document export is provided by the document renderer"))
		return
	}
	if format == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(markdown(v)))
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	_, _ = w.Write(pdf(v))
}
