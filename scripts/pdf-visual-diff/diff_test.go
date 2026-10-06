// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingBlankPageFailsRegardlessOfPixelTolerance(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		if _, err := os.Stat("/opt/homebrew/bin/pdftoppm"); err != nil {
			t.Skip("pdftoppm unavailable")
		}
	}
	dir := t.TempDir()
	for pages := 1; pages <= 2; pages++ {
		kids := "3 0 R"
		if pages == 2 {
			kids += " 4 0 R"
		}
		objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pages)}
		for page := 0; page < pages; page++ {
			objects = append(objects, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>")
		}
		var pdf strings.Builder
		pdf.WriteString("%PDF-1.4\n")
		var offsets []int
		for i, object := range objects {
			offsets = append(offsets, pdf.Len())
			fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
		}
		xref := pdf.Len()
		fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
		for _, offset := range offsets {
			fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
		}
		fmt.Fprintf(&pdf, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(pages, ".pdf")), []byte(pdf.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, missing := range []string{"left", "right"} {
		t.Run(missing, func(t *testing.T) {
			left, right := "1.pdf", "2.pdf"
			if missing == "right" {
				left, right = right, left
			}
			out := filepath.Join(dir, missing)
			code, err := Run(filepath.Join(dir, left), filepath.Join(dir, right), Options{OutDir: out, DPI: 36, Tolerance: 255, Threshold: 100})
			if err != nil {
				t.Fatal(err)
			}
			if code != 2 {
				t.Fatalf("missing blank page exit=%d, want 2", code)
			}
			html, err := os.ReadFile(filepath.Join(out, "summary.html"))
			if err != nil || !strings.Contains(string(html), "missing "+missing) {
				t.Fatalf("missing report: %s %v", html, err)
			}
		})
	}
}

func TestIdenticalSyntheticPDFsMatch(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		if _, statErr := os.Stat("/opt/homebrew/bin/pdftoppm"); statErr != nil {
			t.Skip("pdftoppm unavailable")
		}
	}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "same.pdf")
	if err := os.WriteFile(pdf, rectanglePDF(0, 0, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	code, err := Run(pdf, pdf, Options{OutDir: out, DPI: 150, Tolerance: 16, Threshold: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	html, err := os.ReadFile(filepath.Join(out, "summary.html"))
	if err != nil || len(html) == 0 {
		t.Fatalf("summary missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "page-01-diff.png")); err != nil {
		t.Fatal(err)
	}
}

func TestShiftedRectangleExceedsThreshold(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		if _, statErr := os.Stat("/opt/homebrew/bin/pdftoppm"); statErr != nil {
			t.Skip("pdftoppm unavailable")
		}
	}
	dir := t.TempDir()
	left := filepath.Join(dir, "left.pdf")
	right := filepath.Join(dir, "right.pdf")
	if err := os.WriteFile(left, rectanglePDF(0, 0, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, rectanglePDF(80, 0, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	code, err := Run(left, right, Options{OutDir: out, DPI: 150, Tolerance: 16, Threshold: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatal("expected nonzero exit above threshold")
	}
}

func rectanglePDF(x, y, black int) []byte {
	// A4 media box with one filled rectangle. black=1 paints black, else red.
	color := "1 0 0"
	if black == 1 {
		color = "0 0 0"
	}
	stream := "q " + color + " rg " + itoa(72+x) + " " + itoa(700+y) + " 120 36 re f Q"
	return []byte("%PDF-1.4\n" +
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 595.28 841.89]/Contents 4 0 R>>endobj\n" +
		"4 0 obj<</Length " + itoa(len(stream)) + ">>stream\n" + stream + "\nendstream\nendobj\n" +
		"trailer<</Size 5/Root 1 0 R>>\n%%EOF\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
