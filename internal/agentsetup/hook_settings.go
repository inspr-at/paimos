// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const pairedHookMarker = " # aeon-inbox-hook-v2"

var hookEvents = []string{"PostToolUse", "UserPromptSubmit", "Stop"}
var errHookSettings = errors.New("settings_changed")

func splitPhysicalPath(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

type HookReceipt struct {
	Schema     string  `json:"schema"`
	ComputerID string  `json:"computer_id"`
	Harness    string  `json:"harness"`
	Home       string  `json:"home"`
	Pin        HookPin `json:"pin"`
	// Exact full groups, not a marker suffix, establish ownership. Retaining
	// old groups until the settings commit also makes interrupted repair safe.
	Groups map[string][]json.RawMessage `json:"groups"`
}

func hookGroup(pin HookPin, harness, event string) json.RawMessage {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command := quote(pin.Path) + " hook " + harness + " " + event + " --paired" + pairedHookMarker
	raw, _ := json.Marshal(map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 3}}})
	return raw
}

func sameHookJSON(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	x, _ := json.Marshal(av)
	y, _ := json.Marshal(bv)
	return bytes.Equal(x, y)
}

// Reject duplicate object keys at any depth rather than inspect a different
// effective configuration than the harness (first-key vs last-key parsers).
func uniqueHookJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		t, err := d.Token()
		if err != nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				name, ok := k.(string)
				if e != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value() {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !value() {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim(']')
		default:
			return true
		}
	}
	if !value() {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func aeonHookGroup(raw []byte) bool {
	// Any possible Aeon delivery command is a conflict unless the *whole*
	// group exactly matches the saved receipt. Never include raw text in errors.
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var inspect func(any) bool
	inspect = func(v any) bool {
		switch x := v.(type) {
		case string:
			s := strings.ToLower(x)
			return strings.Contains(s, "aeon-inbox-hook-") || strings.Contains(s, " hook claude ") || strings.Contains(s, " hook codex ") || ((strings.Contains(s, "aeon") || strings.Contains(s, "paimos")) && !strings.Contains(s, " rules "))
		case []any:
			for _, child := range x {
				if inspect(child) {
					return true
				}
			}
		case map[string]any:
			for _, child := range x {
				if inspect(child) {
					return true
				}
			}
		}
		return false
	}
	return inspect(value)
}

func mergePairedHooks(before []byte, receipt HookReceipt, pin HookPin, uninstall bool) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if len(before) > 0 && (!uniqueHookJSON(before) || json.Unmarshal(before, &doc) != nil || doc == nil) {
		return nil, errors.New("config_provenance")
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok && (json.Unmarshal(raw, &hooks) != nil || hooks == nil) {
		return nil, errors.New("config_provenance")
	}
	// Inspect every event; an unexpected event or v1/v2 mixture must not leave
	// a second competing consumer alongside the new integration.
	for event, raw := range hooks {
		var groups []json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			return nil, errors.New("config_provenance")
		}
		kept := make([]json.RawMessage, 0, len(groups))
		for _, group := range groups {
			owned := false
			for _, old := range receipt.Groups[event] {
				if sameHookJSON(group, old) {
					owned = true
					break
				}
			}
			if owned {
				continue
			}
			if aeonHookGroup(group) {
				return nil, errors.New("ownership_unknown")
			}
			kept = append(kept, group)
		}
		if len(kept) == 0 && len(groups) > 0 {
			delete(hooks, event)
		} else {
			hooks[event], _ = json.Marshal(kept)
		}
	}
	if !uninstall {
		for _, event := range hookEvents {
			var groups []json.RawMessage
			_ = json.Unmarshal(hooks[event], &groups)
			groups = append(groups, hookGroup(pin, receipt.Harness, event))
			hooks[event], _ = json.Marshal(groups)
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"], _ = json.Marshal(hooks)
	}
	after, err := json.MarshalIndent(doc, "", "  ")
	return append(after, '\n'), err
}

func (s *Store) readHookSettings(name string, max int64) ([]byte, os.FileMode, error) {
	if !validName(name) {
		return nil, 0, errHookSettings
	}
	fd, err := unix.Openat(int(s.root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, 0600, os.ErrNotExist
	}
	if err != nil {
		return nil, 0, errHookSettings
	}
	f := os.NewFile(uintptr(fd), "hook-settings")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Getuid() || st.Nlink != 1 || st.Mode&0022 != 0 {
		return nil, 0, errHookSettings
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, 0, errHookSettings
	}
	return raw, os.FileMode(st.Mode & 0777), nil
}

// A directory descriptor prevents symlink redirection. The retained lock
// serializes Aeon writers; atomic exchange checks the actual displaced file,
// including an editor replacement after the last snapshot check.
// As elsewhere in setup, this is not isolation from hostile same-UID writers.
func (s *Store) commitHookSettings(name string, before, after []byte, mode os.FileMode, beforeCommit func()) error {
	check := func() bool {
		b, currentMode, err := s.readHookSettings(name, 4<<20)
		return (before == nil && errors.Is(err, os.ErrNotExist)) || (err == nil && before != nil && currentMode == mode && bytes.Equal(b, before))
	}
	if !check() {
		return errHookSettings
	}
	id, err := randomSecret()
	if err != nil {
		return errHookSettings
	}
	tmp := ".aeon-hooks-" + string(id[:16])
	f, err := s.open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return errHookSettings
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = unix.Unlinkat(int(s.root.Fd()), tmp, 0)
		}
	}()
	if _, err = f.Write(after); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errHookSettings
	}
	if before != nil {
		// Backup uses create-only and private mode, including unrelated settings.
		if err = s.Write("hook-backup-"+string(id[:16])+".json", before, true); err != nil {
			return errHookSettings
		}
	}
	if !check() {
		return errHookSettings
	}
	if beforeCommit != nil {
		beforeCommit()
	}
	if before == nil {
		err = renameExclusive(int(s.root.Fd()), tmp, name)
	} else {
		err = exchangeHookSettings(int(s.root.Fd()), tmp, name)
		if err == nil {
			displaced, displacedMode, readErr := s.readHookSettings(tmp, 4<<20)
			if readErr != nil || displacedMode != mode || !bytes.Equal(displaced, before) {
				// Restore the raced editor version when our staged version is
				// still installed. Otherwise retain the displaced inode for
				// local recovery, never delete another writer's bytes.
				current, _, e := s.readHookSettings(name, 4<<20)
				if e != nil || !bytes.Equal(current, after) || exchangeHookSettings(int(s.root.Fd()), tmp, name) != nil {
					removeTemp = false
				}
				if removeTemp {
					staged, _, e := s.readHookSettings(tmp, 4<<20)
					removeTemp = e == nil && bytes.Equal(staged, after)
				}
				_ = unix.Fsync(int(s.root.Fd()))
				return errHookSettings
			}
		}
	}
	if err != nil || unix.Fsync(int(s.root.Fd())) != nil {
		return errHookSettings
	}
	return nil
}

func hookProjectBlocker(workspace, home string) string {
	physical, err := filepath.EvalSymlinks(workspace)
	if err != nil || physical != workspace || !filepath.IsAbs(workspace) {
		return "config_provenance"
	}
	// Inspect the full physical ancestor chain, not just the repository root.
	for dir := workspace; ; dir = filepath.Dir(dir) {
		if dir != home {
			for _, file := range []string{".claude/settings.json", ".claude/settings.local.json", ".codex/hooks.json"} {
				path := filepath.Join(dir, file)
				if info, e := os.Lstat(filepath.Dir(path)); e == nil && info.Mode()&os.ModeSymlink != 0 {
					return "config_provenance"
				}
				if _, e := os.Lstat(path); errors.Is(e, os.ErrNotExist) {
					continue
				}
				d, e := openHookImageDirectory(filepath.Dir(path))
				if e != nil {
					return "config_provenance"
				}
				raw, _, e := d.readHookSettings(filepath.Base(path), 4<<20)
				d.Close()
				if e != nil || !uniqueHookJSON(raw) {
					return "config_provenance"
				}
				var doc map[string]json.RawMessage
				if json.Unmarshal(raw, &doc) != nil || doc == nil {
					return "config_provenance"
				}
				if hooks, ok := doc["hooks"]; ok && aeonHookGroup(hooks) {
					return "project_override"
				}
				for _, k := range []string{"disableAllHooks", "allowManagedHooksOnly"} {
					if string(doc[k]) == "true" {
						return "project_override"
					}
				}
			}
		}
		if dir == "/" {
			break
		}
	}
	return ""
}
