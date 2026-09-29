// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
)

// maxTLDRFile bounds a bulk explanation file; 100 rules of 2×300 bytes fit easily.
const maxTLDRFile = 1 << 20

// tldrEntry is one explanation in a bulk file. Name (for the set) and text
// (for a rule) are optional context copied from --template: when present they
// must still match the draft, so an explanation is never filed under a rule
// whose wording changed since it was written.
type tldrEntry struct {
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
	EN   string `json:"en"`
	DE   string `json:"de,omitempty"`
}

// tldrFile is the bulk file: the set explanation and one entry per rule
// identity. null removes an explanation; identities left out, and template
// entries left blank (no en and no de), stay unchanged.
type tldrFile struct {
	SetID    string                     `json:"set_id,omitempty"`
	Revision int64                      `json:"revision,omitempty"`
	Set      json.RawMessage            `json:"set,omitempty"`
	Rules    map[string]json.RawMessage `json:"rules,omitempty"`
}

// tldrReport is what rules-tldr prints: the plan (dry run) or the receipt.
type tldrReport struct {
	Mode      string   `json:"mode"`
	SetID     string   `json:"set_id"`
	Name      string   `json:"name"`
	Revision  int64    `json:"revision"`
	Set       string   `json:"set,omitempty"`
	Added     []string `json:"added"`
	Changed   []string `json:"changed"`
	Confirmed []string `json:"confirmed"`
	Cleared   []string `json:"cleared"`
	Unchanged int      `json:"unchanged"`
	// Missing lists rules that still have no explanation afterwards; Check lists
	// explanations that still need a person to check them against changed text.
	Missing []string `json:"missing"`
	Check   []string `json:"check"`
	Note    string   `json:"note"`
}

func (rt *runtime) cmdRulesTLDR() *Command {
	return rt.rulesTLDRCommand(rt.api)
}

func (rt *runtime) rulesTLDRCommand(connect func() (*client.Client, error)) *Command {
	var setID, revision, file string
	var apply, template bool
	return &Command{
		Name: "rules-tldr", Short: "Write TL;DR explanations for a rule set and its rules into the draft",
		Use: "rules-tldr --set UUID (--template | --revision N --file tldrs.json [--apply])",
		Long: "Explanations are for people and are never part of what agents receive. --template prints a JSON skeleton of the set's draft to fill in. " +
			"With --file the command is a dry run by default: it reads the draft, checks every entry and prints the plan. --apply writes the explanations into the draft " +
			"(revision-checked); it never publishes, which stays the normal person approval. File shape: " +
			`{"set":{"en":"…","de":"…"},"rules":{"<identity>":{"en":"…","de":"…"}}}; null removes one; "name"/"text" from the template must still match.`,
		minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&setID, "set", 0, "rule set UUID")
			fs.string(&revision, "revision", 0, "expected draft revision (with --file)")
			fs.string(&file, "file", 0, "bulk explanation JSON file")
			fs.bool(&apply, "apply", 0, "write the explanations into the draft (default: dry run)")
			fs.bool(&template, "template", 0, "print a JSON skeleton of the set to fill in")
		},
		run: func([]string) error {
			id := strings.ToLower(strings.TrimSpace(setID))
			if !canonicalRuleUUID(id) {
				return usagef("--set requires the set UUID")
			}
			if template {
				if file != "" || revision != "" || apply {
					return usagef("--template takes only --set")
				}
				api, err := connect()
				if err != nil {
					return err
				}
				set, err := fetchRuleSet(api, id)
				if err != nil {
					return rt.fail(err, api.Token)
				}
				return rt.printJSON(tldrTemplate(set))
			}
			n, err := strconv.ParseInt(revision, 10, 64)
			if err != nil || n < 1 {
				return usagef("--revision requires the draft's positive revision")
			}
			if file == "" {
				return usagef("--file is required (or use --template)")
			}
			in, err := readTLDRFile(file)
			if err != nil {
				return err
			}
			if (in.SetID != "" && strings.ToLower(in.SetID) != id) || (in.Revision != 0 && in.Revision != n) {
				return usagef("the file names a different set or revision than --set/--revision")
			}
			api, err := connect()
			if err != nil {
				return err
			}
			set, err := fetchRuleSet(api, id)
			if err != nil {
				return rt.fail(err, api.Token)
			}
			if set.Revision != n {
				return fmt.Errorf("the draft is at revision %d, not %d; review it and run again with --revision %d", set.Revision, n, set.Revision)
			}
			request, err := tldrRequest(set, in, n)
			if err != nil {
				return err
			}
			next, changed, err := rules.ApplyTLDRs(set, request)
			if err != nil {
				return err
			}
			report := tldrPlan(set, next, request)
			if !changed {
				report.Mode, report.Note = "unchanged", "Nothing to write; the draft already has these explanations."
				return rt.printJSON(report)
			}
			if !apply {
				report.Mode, report.Note = "dry-run", "Nothing was written. Run again with --apply to write these explanations into the draft."
				return rt.printJSON(report)
			}
			var saved rules.Set
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err = api.Do(ctx, http.MethodPut, "/api/rules/sets/"+id+"/tldr", request, &saved); err != nil {
				return rt.fail(err, api.Token)
			}
			report = tldrPlan(set, saved, request)
			report.Mode, report.Revision = "draft", saved.Revision
			report.Note = "Written to the draft. A person publishes it with the normal rules approval; agents never receive explanations."
			return rt.printJSON(report)
		},
	}
}

func canonicalRuleUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}

func fetchRuleSet(api *client.Client, id string) (rules.Set, error) {
	var set rules.Set
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := api.Do(ctx, http.MethodGet, "/api/rules/sets/"+id, nil, &set)
	if err == nil && set.ID != id {
		err = errors.New("the server answered with a different set")
	}
	return set, err
}

func readTLDRFile(path string) (tldrFile, error) {
	var in tldrFile
	f, err := os.Open(path)
	if err != nil {
		return in, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxTLDRFile+1))
	if err != nil {
		return in, err
	}
	if len(raw) > maxTLDRFile {
		return in, usagef("the explanation file must be 1 MiB or smaller")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		return in, usagef("the explanation file is not valid: %v", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return in, usagef("the explanation file must hold one JSON object")
	}
	if len(in.Set) == 0 && len(in.Rules) == 0 {
		return in, usagef("the explanation file names no set or rule explanation")
	}
	return in, nil
}

func decodeEntry(raw json.RawMessage) (*tldrEntry, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var e tldrEntry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, err
	}
	return &e, nil
}

func blank(e *tldrEntry) bool { return strings.TrimSpace(e.EN) == "" && strings.TrimSpace(e.DE) == "" }

// tldrRequest turns the file into the endpoint request, checking the
// template context against the draft.
func tldrRequest(set rules.Set, in tldrFile, revision int64) (rules.TLDRInput, error) {
	out := rules.TLDRInput{ExpectedRevision: revision, Rules: map[string]*rules.TLDRText{}}
	var mismatched []string
	if len(in.Set) > 0 {
		e, err := decodeEntry(in.Set)
		if err != nil {
			return out, usagef("set: %v", err)
		}
		if e == nil {
			out.Set = json.RawMessage("null")
		} else if !blank(e) { // a template entry nobody filled in leaves the set explanation as it is
			if e.Text != "" {
				return out, usagef("set: use name, not text")
			}
			if e.Name != "" && e.Name != set.Name {
				mismatched = append(mismatched, "set name")
			}
			out.Set, _ = json.Marshal(rules.TLDRText{EN: e.EN, DE: e.DE})
		}
	}
	current := map[string]rules.Rule{}
	for _, r := range set.Rules {
		current[r.Identity] = r
	}
	for identity, raw := range in.Rules {
		e, err := decodeEntry(raw)
		if err != nil {
			return out, usagef("rule %s: %v", identity, err)
		}
		if e == nil {
			out.Rules[identity] = nil
			continue
		}
		if blank(e) {
			continue
		}
		if e.Name != "" {
			return out, usagef("rule %s: use text, not name", identity)
		}
		if r, ok := current[identity]; ok && e.Text != "" && e.Text != r.Text {
			mismatched = append(mismatched, identity)
		}
		out.Rules[identity] = &rules.TLDRText{EN: e.EN, DE: e.DE}
	}
	if len(mismatched) > 0 {
		slices.Sort(mismatched)
		return out, fmt.Errorf("the draft's wording changed since the file was written (%s); rewrite those explanations from a fresh --template", strings.Join(mismatched, ", "))
	}
	return out, nil
}

// tldrPlan compares the draft before and after, per rule.
func tldrPlan(before, after rules.Set, in rules.TLDRInput) tldrReport {
	r := tldrReport{SetID: before.ID, Name: before.Name, Revision: before.Revision, Added: []string{}, Changed: []string{}, Confirmed: []string{}, Cleared: []string{}, Missing: []string{}, Check: []string{}}
	classify := func(old, next *rules.TLDR, basis string) string {
		switch {
		case old == nil && next == nil:
			return ""
		case old == nil:
			return "added"
		case next == nil:
			return "cleared"
		case old.EN == next.EN && old.DE == next.DE:
			if old.Basis != basis {
				return "confirmed"
			}
			return "unchanged"
		default:
			return "changed"
		}
	}
	if len(in.Set) > 0 {
		r.Set = classify(before.TLDR, after.TLDR, rules.SetBasis(before.Rules))
	}
	old := map[string]*rules.TLDR{}
	for _, rule := range before.Rules {
		old[rule.Identity] = rule.TLDR
	}
	for _, rule := range after.Rules {
		if _, named := in.Rules[rule.Identity]; named {
			switch classify(old[rule.Identity], rule.TLDR, rules.RuleBasis(rule)) {
			case "added":
				r.Added = append(r.Added, rule.Identity)
			case "changed":
				r.Changed = append(r.Changed, rule.Identity)
			case "confirmed":
				r.Confirmed = append(r.Confirmed, rule.Identity)
			case "cleared":
				r.Cleared = append(r.Cleared, rule.Identity)
			default:
				r.Unchanged++
			}
		}
		if rule.TLDR == nil {
			r.Missing = append(r.Missing, rule.Identity)
		} else if rule.TLDR.Basis != rules.RuleBasis(rule) {
			r.Check = append(r.Check, rule.Identity)
		}
	}
	for _, list := range [][]string{r.Added, r.Changed, r.Confirmed, r.Cleared, r.Missing, r.Check} {
		slices.Sort(list)
	}
	return r
}

// tldrTemplate is a fill-in skeleton: every rule's text for context and its
// current explanation, if any.
func tldrTemplate(set rules.Set) map[string]any {
	entry := func(key, value string, t *rules.TLDR) map[string]string {
		e := map[string]string{key: value, "en": "", "de": ""}
		if t != nil {
			e["en"], e["de"] = t.EN, t.DE
		}
		return e
	}
	ruleEntries := map[string]map[string]string{}
	for _, r := range set.Rules {
		ruleEntries[r.Identity] = entry("text", r.Text, r.TLDR)
	}
	return map[string]any{"set_id": set.ID, "revision": set.Revision, "set": entry("name", set.Name, set.TLDR), "rules": ruleEntries}
}
