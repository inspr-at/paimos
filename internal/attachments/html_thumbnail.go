// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"mime"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"golang.org/x/net/html"
)

func isHTML(contentType string) bool {
	media, params, err := mime.ParseMediaType(contentType)
	return err == nil && media == "text/html" && (params["charset"] == "" || strings.EqualFold(params["charset"], "utf-8"))
}

// htmlTextThumbnail is an inert rendition job, NOT a browser screenshot. It
// has no filesystem/network/process API and evaluates neither HTML nor CSS/JS.
// Input is capped at 64 KiB, tokens at 4096, output at 320x200, and text at
// 8x40 ASCII cells. The supplied reader is the tenant's already opened blob.
// No browser profile, shared renderer storage or authenticated page exists.
func htmlTextThumbnail(parent context.Context, src io.Reader) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 250*time.Millisecond)
	defer cancel()
	z := html.NewTokenizer(io.LimitReader(src, 64<<10))
	z.SetMaxBuf(64 << 10)
	var words []string
	chars := 0
	blocked := ""
	for tokens := 0; tokens < 4096 && chars < 320; tokens++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		t := z.Token()
		if kind == html.StartTagToken {
			switch t.Data {
			case "script", "style", "template", "svg", "math", "noscript":
				if blocked == "" {
					blocked = t.Data
				}
			}
		}
		if kind == html.EndTagToken && t.Data == blocked {
			blocked = ""
			continue
		}
		if kind != html.TextToken || blocked != "" {
			continue
		}
		for _, word := range strings.Fields(t.Data) {
			// Fixed bitmap font, no font resolution or external resources.
			word = strings.Map(func(r rune) rune {
				if r < 32 || r > 126 {
					return '?'
				}
				return r
			}, word)
			if len(word) > 40 {
				word = word[:37] + "..."
			}
			words = append(words, word)
			chars += len(word) + 1
			if chars >= 320 {
				break
			}
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{247, 248, 245, 255}), image.Point{}, draw.Src)
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.RGBA{50, 66, 66, 255}), Face: basicfont.Face7x13, Dot: fixed.P(16, 24)}
	d.DrawString("HTML - text preview")
	line, y := "", 51
	for _, word := range words {
		if len(line)+len(word)+1 > 40 {
			d.Dot = fixed.P(16, y)
			d.DrawString(line)
			y += 17
			line = ""
			if y > 170 {
				break
			}
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if y <= 170 {
		d.Dot = fixed.P(16, y)
		d.DrawString(line)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), ctx.Err()
}
