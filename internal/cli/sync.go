// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// sync check re-renders each skill file that carries a paimos-managed header
// (and each path recorded by skill render) and reports drift. It does not
// contact a classic instance. This command adds no httpapi.Module or plugin manifest.

type renderedSkillEntry struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
	Harness string `json:"harness"`
	Path    string `json:"path"`
}

type renderedSkillIndex struct {
	SchemaVersion string               `json:"schema_version"`
	Entries       []renderedSkillEntry `json:"entries"`
}

type skillCheckRecord struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
	Rev   string `json:"rev,omitempty"`
}

var managedHeaderRE = regexp.MustCompile(`<!-- paimos: rendered from ([^/\s]+)/([^@\s]+)@(\S+) harness=(\S+) -->`)

var skillScanDirs = []string{
	filepath.Join(".claude", "commands"),
	filepath.Join(".agents", "skills"),
	filepath.Join(".grok", "skills"),
	filepath.Join(".pi", "skills"),
	filepath.Join(".cursor", "skills"),
	filepath.Join(".paimos", "skills"),
}

func (rt *runtime) cmdSync() *Command {
	return &Command{
		Name:  "sync",
		Short: "Detect drift in rendered skills",
		Use:   "sync <check>",
		subs:  []*Command{rt.cmdSyncCheck()},
	}
}

func (rt *runtime) cmdSyncCheck() *Command {
	var project, workspace, kind string
	return &Command{
		Name:  "check",
		Short: "Compare rendered skill files with the canonical artifact",
		Use:   "sync check --project KEY",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&workspace, "workspace", 0, "workspace root (default: working directory)")
			fs.string(&kind, "kind", 0, "restrict to skill (the only served kind)")
		},
		run: func(args []string) error {
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			kind = strings.TrimSpace(kind)
			if kind != "" && kind != "skill" {
				return usagef("sync check only detects rendered skill drift")
			}
			root, err := workspaceRoot(workspace)
			if err != nil {
				return err
			}
			proj, err := rt.projectNode(project)
			if err != nil {
				return err
			}
			projectKey := projectDisplayKey(proj, project)
			entries, err := discoverRenderedSkills(root, projectKey)
			if err != nil {
				return err
			}
			records := make([]skillCheckRecord, 0, len(entries))
			cache := map[string][]byte{}
			drift := 0
			for _, entry := range entries {
				raw, ok := cache[entry.Agent]
				if !ok {
					raw, _, err = rt.canonicalArtifact(project, entry.Agent)
					if err != nil {
						return err
					}
					cache[entry.Agent] = raw
				}
				rendered, err := renderThroughHarness(raw, entry.Harness, projectKey, entry.Agent)
				if err != nil {
					return rt.fail(err, "")
				}
				path := resolveRecordedPath(root, entry.Path)
				rec := skillCheckRecord{Kind: "skill", Name: entry.Agent, Path: path, Rev: rendered.Rev}
				existing, readErr := os.ReadFile(path)
				switch {
				case readErr != nil && os.IsNotExist(readErr):
					rec.State = "missing_local"
				case readErr != nil:
					return fmt.Errorf("read %s: %w", path, readErr)
				default:
					switch compareRendered(rendered.Body, string(existing)) {
					case checkIdentical:
						rec.State = "identical"
					case checkHeaderMissing:
						rec.State = "header_missing"
					default:
						rec.State = "diff"
					}
				}
				if rec.State != "identical" {
					drift++
				}
				records = append(records, rec)
			}
			if rt.jsonOut {
				if err := rt.printJSON(map[string]any{
					"project_id":  proj.ID,
					"project_key": projectKey,
					"records":     records,
					"drift_count": drift,
				}); err != nil {
					return err
				}
			} else {
				for _, rec := range records {
					fmt.Fprintf(rt.stdout, "%-15s %s/%s -> %s\n", rec.State, rec.Kind, rec.Name, rec.Path)
				}
				if drift == 0 {
					fmt.Fprintln(rt.stdout, "(no drift)")
				} else {
					fmt.Fprintf(rt.stdout, "%d artifact(s) in drift\n", drift)
				}
			}
			if drift > 0 {
				return &exitError{code: 1}
			}
			return nil
		},
	}
}

func skillIndexPath(workspace string) string {
	return filepath.Join(workspace, ".paimos", "rendered-skills.json")
}

func readSkillIndex(path string) (renderedSkillIndex, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return renderedSkillIndex{SchemaVersion: "1", Entries: []renderedSkillEntry{}}, nil
		}
		return renderedSkillIndex{}, err
	}
	var idx renderedSkillIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return renderedSkillIndex{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if idx.SchemaVersion == "" {
		idx.SchemaVersion = "1"
	}
	if idx.Entries == nil {
		idx.Entries = []renderedSkillEntry{}
	}
	return idx, nil
}

func upsertRenderedSkill(workspace string, entry renderedSkillEntry) error {
	path := skillIndexPath(workspace)
	idx, err := readSkillIndex(path)
	if err != nil {
		return err
	}
	replaced := false
	for i, old := range idx.Entries {
		if old.Project == entry.Project && old.Agent == entry.Agent && old.Harness == entry.Harness {
			idx.Entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Entries = append(idx.Entries, entry)
	}
	slices.SortFunc(idx.Entries, func(a, b renderedSkillEntry) int {
		if a.Project != b.Project {
			return strings.Compare(a.Project, b.Project)
		}
		if a.Agent != b.Agent {
			return strings.Compare(a.Agent, b.Agent)
		}
		return strings.Compare(a.Harness, b.Harness)
	})
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeRenderedInWorkspace(workspace, path, string(raw))
}

func discoverRenderedSkills(workspace, projectKey string) ([]renderedSkillEntry, error) {
	idx, err := readSkillIndex(skillIndexPath(workspace))
	if err != nil {
		return nil, err
	}
	var found []renderedSkillEntry
	seen := map[string]bool{}
	add := func(entry renderedSkillEntry) {
		if entry.Project != projectKey || entry.Agent == "" || entry.Harness == "" || entry.Path == "" {
			return
		}
		key := entry.Agent + "\x00" + entry.Harness + "\x00" + filepath.Clean(resolveRecordedPath(workspace, entry.Path))
		if seen[key] {
			return
		}
		seen[key] = true
		found = append(found, entry)
	}
	for _, entry := range idx.Entries {
		add(entry)
	}
	for _, rel := range skillScanDirs {
		dir := filepath.Join(workspace, rel)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return nil
			}
			entry, ok, err := headerEntry(path)
			if err != nil || !ok {
				return err
			}
			stored, relErr := filepath.Rel(workspace, path)
			if relErr == nil && filepath.IsLocal(stored) {
				entry.Path = filepath.ToSlash(stored)
			} else {
				entry.Path = path
			}
			add(entry)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(found, func(a, b renderedSkillEntry) int {
		if a.Agent != b.Agent {
			return strings.Compare(a.Agent, b.Agent)
		}
		if a.Harness != b.Harness {
			return strings.Compare(a.Harness, b.Harness)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return found, nil
}

func headerEntry(path string) (renderedSkillEntry, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return renderedSkillEntry{}, false, err
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return renderedSkillEntry{}, false, err
	}
	m := managedHeaderRE.FindStringSubmatch(string(buf[:n]))
	if len(m) != 5 {
		return renderedSkillEntry{}, false, nil
	}
	return renderedSkillEntry{Project: m[1], Agent: m[2], Harness: m[4], Path: path}, true, nil
}

func resolveRecordedPath(workspace, stored string) string {
	if filepath.IsAbs(stored) {
		return filepath.Clean(stored)
	}
	return filepath.Join(workspace, filepath.FromSlash(stored))
}
