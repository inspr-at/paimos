// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

// Run reads only stdin and an optional explicit local checkpoint. It performs no
// submissions and launches no processes. Save the complete result before POSTing
// individual reports; retry those exact report bodies after an uncertain result.
func Run(args []string, in io.Reader, out, errOut io.Writer) error {
	opt, path, err := parseArgs(args)
	if err != nil {
		return err
	}
	if path != "" {
		opt.Previous, err = readCheckpoint(path)
		if err != nil {
			return err
		}
	}
	result, err := Parse(in, opt)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return fmt.Errorf("write normalized usage failed")
	}
	return nil
}

const help = "session-usage-parse --source codex|cursor|gemini|opencode --session-id UUID --source-session-id ID (--from-start | --checkpoint-file PATH) [--model ID] [--final] [--billing-mode unknown|api|subscription] [--subscription-label TEXT] [--account-id UUID] [--account-label TEXT]; stdin is a complete metadata-only capture from session start; deltas require stable record identities"

func parseArgs(args []string) (Options, string, error) {
	var opt Options
	var path string
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return opt, "", &UsageError{Msg: "duplicate argument"}
		}
		seen[flag] = true
		switch flag {
		case "--help", "-h":
			return opt, "", &UsageError{Msg: help}
		case "--from-start":
			opt.FromStart = true
			continue
		case "--final":
			opt.Final = true
			continue
		}
		var dest *string
		switch flag {
		case "--source":
			dest = &opt.Source
		case "--session-id":
			dest = &opt.SessionID
		case "--source-session-id":
			dest = &opt.SourceSessionID
		case "--model":
			dest = &opt.Model
		case "--billing-mode":
			dest = &opt.BillingMode
		case "--subscription-label":
			dest = &opt.SubscriptionLabel
		case "--account-id":
			dest = &opt.AccountID
		case "--account-label":
			dest = &opt.AccountLabel
		case "--checkpoint-file":
			dest = &path
		default:
			return opt, "", &UsageError{Msg: "unsupported argument"}
		}
		i++
		if i >= len(args) || args[i] == "" || len(args[i]) > 1024 || strings.ContainsAny(args[i], "\r\n\x00") {
			return opt, "", &UsageError{Msg: "argument requires a valid value"}
		}
		*dest = args[i]
	}
	if opt.Source != "codex" && opt.Source != "cursor" && opt.Source != "gemini" && opt.Source != "opencode" {
		return opt, "", &UsageError{Msg: "--source must be codex, cursor, gemini or opencode"}
	}
	return opt, path, nil
}

func readCheckpoint(path string) (*Checkpoint, error) {
	return readCheckpointWithIO(path, openCheckpointFile, io.ReadAll)
}

func readCheckpointWithIO(path string, openFile func(string) (*os.File, error), readAll func(io.Reader) ([]byte, error)) (*Checkpoint, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, &UsageError{Msg: "checkpoint path invalid"}
	}
	if forbiddenCheckpointPath(path) || forbiddenCheckpointPath(abs) {
		return nil, &UsageError{Msg: "checkpoint path forbidden"}
	}
	f, err := openFile(abs)
	if err != nil {
		return nil, &UsageError{Msg: "checkpoint unreadable"}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 1024 {
		return nil, &UsageError{Msg: "checkpoint must be a regular file of at most 1KiB"}
	}
	raw, err := readAll(io.LimitReader(f, 1025))
	if err != nil || len(raw) > 1024 || int64(len(raw)) != info.Size() {
		return nil, &UsageError{Msg: "checkpoint unreadable"}
	}
	return ParseCheckpoint(raw)
}

// Check the original spelling too: cleaning must not erase a forbidden component.
func forbiddenCheckpointPath(path string) bool {
	// Share the importer policy so newly protected vendor stores stay refused
	// before any checkpoint descriptor is opened, including unclean spellings.
	if rulesimport.Prohibited(path) != nil {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		name := strings.ToLower(part)
		if strings.HasPrefix(name, ".env") || strings.HasPrefix(name, "id_") {
			return true
		}
		for _, suffix := range []string{".env", ".key", ".age", ".gpg"} {
			if strings.HasSuffix(name, suffix) {
				return true
			}
		}
		switch name {
		case ".ssh", ".inspr", ".codex", ".cursor", ".aws", ".gnupg", "secrets", "credentials", "auth.json":
			return true
		}
	}
	return false
}
