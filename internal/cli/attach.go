// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

type attachmentView struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
}

func (rt *runtime) cmdAttach() *Command {
	var issue string
	var download string
	root := &Command{Name: "attach", Short: "Upload and manage node attachments", Use: "attach <issue-ref> <file>", minArgs: 2, maxArgs: 2,
		run: func(args []string) error {
			n, err := rt.nodeRef(args[0])
			if err != nil {
				return err
			}
			f, err := os.Open(args[1])
			if err != nil {
				return rt.fail(err, "")
			}
			defer f.Close()
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, err := mw.CreateFormFile("file", filepath.Base(args[1]))
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(part, f, 50<<20+1)
			if copyErr != nil && copyErr != io.EOF {
				return copyErr
			}
			if written > 50<<20 {
				return usagef("attachment is too large")
			}
			if err := mw.Close(); err != nil {
				return err
			}
			raw, err := rt.rawAPI(http.MethodPost, "/api/nodes/"+url.PathEscape(n.ID)+"/attachments", body.Bytes(), mw.FormDataContentType())
			if err != nil {
				return err
			}
			var created []attachmentView
			if err := json.Unmarshal(raw, &created); err != nil {
				return rt.fail(err, "")
			}
			if len(created) != 1 {
				return rt.fail(fmt.Errorf("upload response contained %d attachments", len(created)), "")
			}
			if rt.jsonOut {
				return rt.printJSON(created[0])
			}
			_, err = fmt.Fprintf(rt.stdout, "✓ attached %s (#%s) to %s\n", created[0].Name, created[0].ID, args[0])
			return err
		},
	}
	root.subs = []*Command{
		{Name: "list", Short: "List attachments on an issue", Use: "attach list --issue REF", addFlags: func(fs *flagSet) { fs.string(&issue, "issue", 0, "issue key") }, run: func([]string) error {
			if issue == "" {
				return usagef("--issue is required")
			}
			n, err := rt.nodeRef(issue)
			if err != nil {
				return err
			}
			var items []attachmentView
			if err := rt.do(http.MethodGet, "/api/nodes/"+url.PathEscape(n.ID)+"/attachments", nil, &items); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(items)
			}
			if len(items) == 0 {
				_, err = fmt.Fprintln(rt.stdout, "(no attachments)")
				return err
			}
			for _, a := range items {
				if _, err = fmt.Fprintf(rt.stdout, "%s  %s  %d bytes\n", a.ID, a.Name, a.Size); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "get", Short: "Get attachment metadata or bytes", Use: "attach get <id> [--download PATH]", minArgs: 1, maxArgs: 1,
			addFlags: func(fs *flagSet) { fs.string(&download, "download", 0, "write bytes to path or - for stdout") }, run: func(args []string) error {
				if !validUUID(args[0]) {
					return usagef("attachment id must be an Aeon UUID")
				}
				if download == "-" && rt.jsonOut {
					return usagef("--json cannot be combined with --download -")
				}
				meta, err := rt.findAttachment(args[0])
				if err != nil {
					return err
				}
				if download != "" {
					if err := rt.downloadAttachment(meta, download); err != nil {
						return err
					}
					if download == "-" {
						return nil
					}
					if rt.jsonOut {
						return rt.printJSON(map[string]any{"attachment": meta, "downloaded_to": download})
					}
					_, err = fmt.Fprintf(rt.stdout, "✓ downloaded %s (#%s) to %s\n", meta.Name, meta.ID, download)
					return err
				}
				if rt.jsonOut {
					return rt.printJSON(meta)
				}
				_, err = fmt.Fprintf(rt.stdout, "%s  %s  %d bytes\n", meta.ID, meta.Name, meta.Size)
				return err
			}},
		{Name: "rm", Short: "Delete an attachment", Use: "attach rm <id>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
			if !validUUID(args[0]) {
				return usagef("attachment id must be an Aeon UUID")
			}
			if err := rt.do(http.MethodDelete, "/api/attachments/"+args[0], nil, nil); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(map[string]any{"deleted": true, "id": args[0]})
			}
			_, err := fmt.Fprintf(rt.stdout, "✓ deleted attachment #%s\n", args[0])
			return err
		}},
	}
	return root
}

func (rt *runtime) findAttachment(id string) (attachmentView, error) {
	var meta attachmentView
	err := rt.do(http.MethodGet, "/api/attachments/"+url.PathEscape(id), nil, &meta)
	return meta, err
}

// Verify bytes in a unique private spool before publishing any output. A file
// destination uses a spool in the same directory so rename is atomic.
func (rt *runtime) downloadAttachment(meta attachmentView, destination string) error {
	if meta.Size < 0 || meta.Size == math.MaxInt64 {
		return fmt.Errorf("invalid attachment size")
	}
	resp, key, err := rt.rawResponse(http.MethodGet, "/api/attachments/"+url.PathEscape(meta.ID)+"/content", nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, err = readRawResponse(resp, maxRawResponseBytes)
		return rt.fail(err, key)
	}
	defer resp.Body.Close()
	dir := ""
	if destination != "-" {
		dir = filepath.Dir(destination)
	}
	tmp, err := os.CreateTemp(dir, ".aeon-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, meta.Size+1))
	if err != nil {
		return rt.fail(err, key)
	}
	if n != meta.Size {
		return rt.fail(fmt.Errorf("incomplete attachment download: got %d bytes, expected %d", n, meta.Size), key)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if destination == "-" {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = io.Copy(rt.stdout, tmp)
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), destination)
}
