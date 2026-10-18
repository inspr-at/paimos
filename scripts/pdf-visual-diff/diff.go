// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
)

// Options controls rasterisation and the mismatch gate.
type Options struct {
	OutDir    string
	DPI       int
	Tolerance int
	Threshold float64
}

// PageReport is one aligned page pair.
type PageReport struct {
	Page            int
	MismatchPercent float64
	SSIM            float64
	Width           int
	Height          int
	DiffPNG         string
	Missing         string
}

// Run renders both PDFs, aligns pages by index, and writes a summary.
// Exit code 2 means a missing page or pixel mismatch above the threshold.
func Run(leftPath, rightPath string, opt Options) (int, error) {
	if opt.DPI <= 0 {
		return 1, fmt.Errorf("dpi must be positive")
	}
	if opt.Tolerance < 0 || opt.Tolerance > 255 {
		return 1, fmt.Errorf("tolerance must be 0..255")
	}
	if err := os.MkdirAll(opt.OutDir, 0o755); err != nil {
		return 1, err
	}
	left, err := rasterise(leftPath, opt.DPI)
	if err != nil {
		return 1, err
	}
	right, err := rasterise(rightPath, opt.DPI)
	if err != nil {
		return 1, err
	}
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	reports := make([]PageReport, 0, n)
	over := false
	for i := 0; i < n; i++ {
		report := PageReport{Page: i + 1}
		var a, b *image.NRGBA
		switch {
		case i >= len(left):
			report.Missing = "left"
			b = right[i]
		case i >= len(right):
			report.Missing = "right"
			a = left[i]
		default:
			a, b = left[i], right[i]
		}
		alignedA, alignedB := align(a, b)
		report.Width = alignedA.Bounds().Dx()
		report.Height = alignedA.Bounds().Dy()
		report.MismatchPercent = mismatchPercent(alignedA, alignedB, opt.Tolerance)
		report.SSIM = ssim(alignedA, alignedB)
		name := fmt.Sprintf("page-%02d-diff.png", report.Page)
		report.DiffPNG = name
		if err := writePNG(filepath.Join(opt.OutDir, name), highlight(alignedA, alignedB, opt.Tolerance)); err != nil {
			return 1, err
		}
		if report.Missing != "" || report.MismatchPercent > opt.Threshold {
			over = true
		}
		reports = append(reports, report)
		fmt.Printf("page %d mismatch=%.4f%% ssim=%.6f\n", report.Page, report.MismatchPercent, report.SSIM)
	}
	if err := os.WriteFile(filepath.Join(opt.OutDir, "summary.html"), []byte(summaryHTML(reports, opt)), 0o644); err != nil {
		return 1, err
	}
	if over {
		return 2, nil
	}
	return 0, nil
}

func align(a, b *image.NRGBA) (*image.NRGBA, *image.NRGBA) {
	if a == nil {
		a = image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	if b == nil {
		b = image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	w := a.Bounds().Dx()
	if b.Bounds().Dx() > w {
		w = b.Bounds().Dx()
	}
	h := a.Bounds().Dy()
	if b.Bounds().Dy() > h {
		h = b.Bounds().Dy()
	}
	return pad(a, w, h), pad(b, w, h)
}

func pad(src *image.NRGBA, w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = 255, 255, 255, 255
	}
	sb := src.Bounds()
	for y := 0; y < sb.Dy() && y < h; y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+sb.Dx()*4], src.Pix[(sb.Min.Y+y)*src.Stride+sb.Min.X*4:(sb.Min.Y+y)*src.Stride+sb.Min.X*4+sb.Dx()*4])
	}
	return dst
}

func mismatchPercent(a, b *image.NRGBA, tolerance int) float64 {
	n := a.Bounds().Dx() * a.Bounds().Dy()
	if n == 0 {
		return 100
	}
	bad := 0
	for i := 0; i < len(a.Pix); i += 4 {
		if channelDelta(a.Pix[i:i+4], b.Pix[i:i+4]) > tolerance {
			bad++
		}
	}
	return 100 * float64(bad) / float64(n)
}

func channelDelta(a, b []byte) int {
	d := abs(int(a[0]) - int(b[0]))
	if v := abs(int(a[1]) - int(b[1])); v > d {
		d = v
	}
	if v := abs(int(a[2]) - int(b[2])); v > d {
		d = v
	}
	return d
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func highlight(a, b *image.NRGBA, tolerance int) *image.NRGBA {
	dst := image.NewNRGBA(a.Bounds())
	for i := 0; i < len(a.Pix); i += 4 {
		if channelDelta(a.Pix[i:i+4], b.Pix[i:i+4]) > tolerance {
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = 220, 40, 40, 255
			continue
		}
		y := uint8((int(a.Pix[i])*299 + int(a.Pix[i+1])*587 + int(a.Pix[i+2])*114) / 1000)
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = y, y, y, 255
	}
	return dst
}

// ssim is the global structural similarity of luminance. It is one pass and
// does not add a dependency.
func ssim(a, b *image.NRGBA) float64 {
	n := float64(a.Bounds().Dx() * a.Bounds().Dy())
	if n == 0 {
		return 0
	}
	var sumA, sumB float64
	lumA := make([]float64, int(n))
	lumB := make([]float64, int(n))
	for i, p := 0, 0; i < len(a.Pix); i, p = i+4, p+1 {
		lumA[p] = (float64(a.Pix[i])*0.299 + float64(a.Pix[i+1])*0.587 + float64(a.Pix[i+2])*0.114)
		lumB[p] = (float64(b.Pix[i])*0.299 + float64(b.Pix[i+1])*0.587 + float64(b.Pix[i+2])*0.114)
		sumA += lumA[p]
		sumB += lumB[p]
	}
	muA, muB := sumA/n, sumB/n
	var varA, varB, cov float64
	for i := range lumA {
		da, db := lumA[i]-muA, lumB[i]-muB
		varA += da * da
		varB += db * db
		cov += da * db
	}
	varA /= n
	varB /= n
	cov /= n
	const (
		c1 = (0.01 * 255) * (0.01 * 255)
		c2 = (0.03 * 255) * (0.03 * 255)
	)
	num := (2*muA*muB + c1) * (2*cov + c2)
	den := (muA*muA + muB*muB + c1) * (varA + varB + c2)
	if den == 0 {
		return 1
	}
	return num / den
}

func summaryHTML(reports []PageReport, opt Options) string {
	var buf bytes.Buffer
	buf.WriteString("<!doctype html><meta charset=utf-8><title>PDF visual diff</title>")
	buf.WriteString("<style>body{font:14px sans-serif;margin:24px;color:#1e2727}table{border-collapse:collapse}td,th{border:1px solid #dfe6e5;padding:6px 10px;text-align:left}img{max-width:480px}</style>")
	fmt.Fprintf(&buf, "<h1>PDF visual diff</h1><p>%d dpi, channel tolerance %d, threshold %.4f%%</p><table><tr><th>Page</th><th>Mismatch %%</th><th>SSIM</th><th>Size</th><th>Diff</th></tr>", opt.DPI, opt.Tolerance, opt.Threshold)
	for _, report := range reports {
		note := ""
		if report.Missing != "" {
			note = " missing " + report.Missing
		}
		fmt.Fprintf(&buf, "<tr><td>%d%s</td><td>%.4f</td><td>%.6f</td><td>%d×%d</td><td><a href=\"%s\"><img src=\"%s\" alt=\"page %d diff\"></a></td></tr>",
			report.Page, note, report.MismatchPercent, report.SSIM, report.Width, report.Height, report.DiffPNG, report.DiffPNG, report.Page)
	}
	buf.WriteString("</table>")
	return buf.String()
}

func rasterise(pdf string, dpi int) ([]*image.NRGBA, error) {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil {
		for _, candidate := range []string{"/opt/homebrew/bin/pdftoppm", "/usr/bin/pdftoppm"} {
			if _, statErr := os.Stat(candidate); statErr == nil {
				bin = candidate
				err = nil
				break
			}
		}
	}
	if err != nil || bin == "" {
		return nil, fmt.Errorf("pdftoppm not found")
	}
	dir, err := os.MkdirTemp("", "pdf-visual-diff-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "page")
	cmd := exec.Command(bin, "-png", "-r", strconv.Itoa(dpi), pdf, prefix)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm: %w", err)
	}
	matches, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	out := make([]*image.NRGBA, 0, len(matches))
	for _, name := range matches {
		img, err := readPNG(name)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pdftoppm produced no pages")
	}
	return out, nil
}

func readPNG(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	return toNRGBA(img), nil
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			dst.SetNRGBA(x-b.Min.X, y-b.Min.Y, c)
		}
	}
	return dst
}

func writePNG(path string, img *image.NRGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
