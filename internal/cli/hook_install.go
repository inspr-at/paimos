// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const inboxHookMarker = " # aeon-inbox-hook-v1"

func (rt *runtime) cmdHookInstall(uninstall bool) *Command {
	name := "install"
	if uninstall {
		name = "uninstall"
	}
	var harness, scope string
	var dryRun bool
	scope = "user"
	return &Command{Name: name, Short: "Merge or remove Aeon inbox hooks", Use: "hook " + name + " --harness claude|codex [--scope user|project] [--dry-run]", maxArgs: 0, addFlags: func(fs *flagSet) {
		fs.string(&harness, "harness", 0, "claude or codex")
		fs.string(&scope, "scope", 0, "user (default) or project (current directory)")
		fs.bool(&dryRun, "dry-run", 0, "print the owned hook changes without writing")
	}, run: func([]string) error {
		if harness != "claude" && harness != "codex" {
			return usagef("--harness must be claude or codex")
		}
		if scope != "user" && scope != "project" {
			return usagef("--scope must be user or project")
		}
		base, err := os.Getwd()
		if scope == "user" {
			base, err = os.UserHomeDir()
		}
		if err != nil {
			return err
		}
		dir, file := ".claude", "settings.json"
		if harness == "codex" {
			dir, file = ".codex", "hooks.json"
		}
		path := filepath.Join(base, dir, file)
		if scope == "user" {
			variable := "CLAUDE_CONFIG_DIR"
			if harness == "codex" {
				variable = "CODEX_HOME"
			}
			if custom := os.Getenv(variable); custom != "" {
				path = filepath.Join(custom, file)
			}
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		command := shellHookQuote(executable)
		config, err := rt.configFile()
		if err != nil {
			return err
		}
		config, err = filepath.Abs(config)
		if err != nil {
			return err
		}
		command += " --config " + shellHookQuote(config)

		if rt.instance != "" {
			command += " --instance " + shellHookQuote(rt.instance)
		}
		command += " hook " + harness + " "
		return rt.mergeInboxHooks(path, command, harness, uninstall, dryRun)
	}}
}

func shellHookQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (rt *runtime) mergeInboxHooks(path, command, harness string, uninstall, dryRun bool) error {
	before, mode, err := readHookSettings(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot read regular owned hook settings")
	}

	doc := map[string]json.RawMessage{}
	if exists && (json.Unmarshal(before, &doc) != nil || doc == nil) {
		return errors.New("hook settings must contain a JSON object")
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok {
		if json.Unmarshal(raw, &hooks) != nil || hooks == nil {
			return errors.New("hooks must be a JSON object")
		}
	}
	var changes []string
	for _, event := range inboxHookEvents() {
		var groups []map[string]json.RawMessage
		if raw, ok := hooks[event]; ok {
			if json.Unmarshal(raw, &groups) != nil {
				return errors.New("hook event must be an array")
			}
		}
		var kept []map[string]json.RawMessage
		// Remove only handlers signed with our command suffix, including old binary paths.
		for _, group := range groups {
			var handlers []map[string]json.RawMessage
			if json.Unmarshal(group["hooks"], &handlers) != nil {
				return errors.New("hook group must contain a hooks array")
			}
			remaining := make([]map[string]json.RawMessage, 0, len(handlers))
			removed := false
			for _, handler := range handlers {
				var cmd, typ string
				_ = json.Unmarshal(handler["command"], &cmd)
				_ = json.Unmarshal(handler["type"], &typ)
				if typ == "command" && strings.HasSuffix(cmd, inboxHookMarker) {
					removed = true
					changes = append(changes, "- "+event+": Aeon inbox hook")
				} else {
					remaining = append(remaining, handler)
				}
			}
			if !removed {
				kept = append(kept, group)
				continue
			}
			if len(remaining) > 0 {
				group["hooks"], _ = json.Marshal(remaining)
				kept = append(kept, group)
			}
		}
		if !uninstall {
			cmd := command + event + inboxHookMarker
			handler := map[string]any{"type": "command", "command": cmd, "timeout": 3}
			if harness == "codex" && event != "Stop" {
				handler["additionalContextLimit"] = 0
			}
			raw, _ := json.Marshal([]any{handler})
			kept = append(kept, map[string]json.RawMessage{"hooks": raw})
			changes = append(changes, "+ "+event+": "+cmd+" (timeout 3s)")
		}
		if len(kept) == 0 {
			// Preserve pre-existing empty event arrays.
			if len(groups) > 0 {
				delete(hooks, event)
			}
		} else {
			hooks[event], _ = json.Marshal(kept)
		}
	}
	if uninstall && len(changes) == 0 {
		_, err := fmt.Fprintln(rt.stdout, "Aeon inbox hooks unchanged.")
		return err
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"], _ = json.Marshal(hooks)
	}
	after, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return errors.New("cannot encode hook settings")
	}
	after = append(after, '\n')
	// Compare decoded JSON too: key order or whitespace is not a configuration change.
	var a, b any
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	canonicalA, _ := json.Marshal(a)
	canonicalB, _ := json.Marshal(b)
	if bytes.Equal(canonicalA, canonicalB) || (!exists && uninstall) {
		_, err := fmt.Fprintln(rt.stdout, "Aeon inbox hooks unchanged.")
		return err
	}
	// Do not dump the settings file: unrelated entries can contain credentials.
	if _, err := fmt.Fprintln(rt.stdout, "Hook changes: "+path); err != nil {
		return err
	}
	for _, change := range changes {
		if _, err := fmt.Fprintln(rt.stdout, change); err != nil {
			return err
		}
	}
	if dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("cannot create hook settings directory")
	}
	// Refuse concurrent replacement instead of overwriting another operator's edit.
	current, _, err := readHookSettings(path)
	if (exists && (err != nil || !bytes.Equal(current, before))) || (!exists && !errors.Is(err, os.ErrNotExist)) {
		return errors.New("hook settings changed; retry")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aeon-hooks-*")
	if err != nil {
		return errors.New("cannot stage hook settings")
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(after); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.New("cannot replace hook settings")
	}
	return nil
}

func readHookSettings(path string) ([]byte, os.FileMode, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, 0600, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0600, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, 0600, errors.New("settings exceed size limit")
	}
	return raw, info.Mode().Perm(), nil
}
