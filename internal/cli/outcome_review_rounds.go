// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type reviewRoundWire struct {
	Ticket   string   `json:"ticket"`
	Kind     string   `json:"kind"`
	Round    int      `json:"round"`
	Verdict  string   `json:"verdict"`
	Rules    []string `json:"cited_rules"`
	At       string   `json:"at"`
	Role     string   `json:"reviewer_role"`
	Artifact string   `json:"artifact"`
	Run      string   `json:"review_run_id,omitempty"`
	Source   string   `json:"source"`
}

func (rt *runtime) cmdOutcomeReviewRound() *Command {
	var in reviewRoundWire
	var round, rules, key string
	return &Command{Name: "review-round", Short: "Record a gate or design round until ok", Use: "outcome review-round --ticket KEY --kind design|gate --round N --verdict ok|changes --at RFC3339 --artifact REF --reviewer-role ROLE --idempotency-key KEY", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&in.Ticket, "ticket", 0, "ticket key or UUID")
			fs.string(&in.Kind, "kind", 0, "gate or design")
			fs.string(&round, "round", 0, "review round, 1 to 999")
			fs.string(&in.Verdict, "verdict", 0, "ok or changes")
			fs.string(&rules, "cited-rules", 0, "comma-separated lens rule IDs; empty is allowed")
			fs.string(&in.At, "at", 0, "observation timestamp, RFC3339")
			fs.string(&in.Role, "reviewer-role", 0, "reviewer, design_reviewer or gate_reviewer; never a name")
			fs.string(&in.Artifact, "artifact", 0, "design artifact reference or PR head SHA")
			fs.string(&in.Run, "review-run", 0, "own submitted review run UUID; required for agents")
			fs.string(&key, "idempotency-key", 0, "stable retry key")
		}, run: func(args []string) error {
			var err error
			in.Round, err = strconv.Atoi(round)
			if err != nil || in.Round < 1 || in.Round > 999 || in.Ticket == "" || in.Artifact == "" || in.Kind != "design" && in.Kind != "gate" || in.Verdict != "ok" && in.Verdict != "changes" {
				return usagef("ticket, kind, round, verdict and artifact are required and must be valid")
			}
			if _, err := time.Parse(time.RFC3339Nano, in.At); err != nil {
				return usagef("--at must be RFC3339")
			}
			if in.Role != "reviewer" && in.Role != "design_reviewer" && in.Role != "gate_reviewer" {
				return usagef("--reviewer-role must be a reviewer role")
			}
			if !outcomeKeyOK(key) || strings.HasPrefix(key, "auto:") {
				return usagef("invalid --idempotency-key")
			}
			in.Rules = []string{}
			if strings.TrimSpace(rules) != "" {
				in.Rules = strings.Split(rules, ",")
				for i := range in.Rules {
					in.Rules[i] = strings.TrimSpace(in.Rules[i])
				}
			}
			in.Source = "recorded"
			out, err := rt.postReviewRound(in, key)
			if err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			fmt.Fprintf(rt.stdout, "recorded review_round %s %s %d\n", in.Ticket, in.Kind, in.Round)
			return nil
		}}
}

func (rt *runtime) postReviewRound(in reviewRoundWire, key string) (map[string]any, error) {
	var out map[string]any
	err := rt.doHeaders(http.MethodPost, "/api/outcomes/review-rounds", in, &out, map[string]string{"Idempotency-Key": key})
	return out, err
}

func (rt *runtime) cmdOutcomeImportDesignRounds() *Command {
	var file string
	var dry bool
	return &Command{Name: "import-design-rounds", Short: "Import a supplied copy of the design review TSV", Use: "outcome import-design-rounds --file PATH [--dry-run]", Long: "Columns: key, repo, first look, rounds until ok, note. Dry-run validates the entire file without requests. Apply requires a person with outcome.write. Each ticket/round is immutable and idempotent. The first-look timestamp is retained for all rounds because the TSV has no later timestamps. Notes contribute lens rule IDs only, never names or prose.", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&file, "file", 0, "copy of the tab-separated source file")
			fs.bool(&dry, "dry-run", 0, "validate and show the plan without writing")
		}, run: func(args []string) error {
			if file == "" {
				return usagef("--file is required")
			}
			f, err := os.Open(file)
			if err != nil {
				return fmt.Errorf("open supplied review TSV: %w", err)
			}
			defer f.Close()
			plan, err := designRoundPlan(f)
			if err != nil {
				return err
			}
			if dry {
				if rt.jsonOut {
					return rt.printJSON(map[string]any{"dry_run": true, "rounds": plan, "count": len(plan)})
				}
				fmt.Fprintf(rt.stdout, "dry-run: %d design round outcomes; no writes\n", len(plan))
				return nil
			}
			for i, in := range plan {
				// Natural identity is enforced by the server, even with another key
				// or a ticket alias. A conflicting import must never be overwritten.
				sum := sha256.Sum256([]byte(in.Ticket + ":design:" + strconv.Itoa(in.Round)))
				if _, err := rt.postReviewRound(in, "design-tsv:"+hex.EncodeToString(sum[:])); err != nil {
					return fmt.Errorf("import stopped after %d of %d outcomes; retry is idempotent: %w", i, len(plan), err)
				}
			}
			if rt.jsonOut {
				return rt.printJSON(map[string]any{"dry_run": false, "count": len(plan)})
			}
			fmt.Fprintf(rt.stdout, "imported or already recorded: %d design round outcomes\n", len(plan))
			return nil
		}}
}

var importRuleIDs = regexp.MustCompile(`\b[UA][1-9][0-9]{0,2}\b`)

func designRoundPlan(source io.Reader) ([]reviewRoundWire, error) {
	const maxBytes = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(source, maxBytes+1))
	if err != nil || len(raw) > maxBytes {
		return nil, usagef("review TSV cannot exceed 1 MiB")
	}
	r := csv.NewReader(strings.NewReader(string(raw)))
	r.Comma = '\t'
	r.FieldsPerRecord = 5
	header, err := r.Read()
	if err != nil || strings.Join(header, "\t") != "key\trepo\tfirst look\trounds until ok\tnote" {
		return nil, usagef("review TSV requires columns: key, repo, first look, rounds until ok, note")
	}
	plan := []reviewRoundWire{}
	seen := map[string]string{}
	for rowNumber := 2; ; rowNumber++ {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || rowNumber > 1001 {
			return nil, usagef("invalid review TSV row %d; at most 1000 tickets", rowNumber)
		}
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}
		n, err := strconv.Atoi(row[3])
		if err != nil || n < 1 || n > 999 || row[0] == "" || len(row[0]) > 128 || row[1] == "" || len(row[1])+len(row[0])+1 > 200 {
			return nil, usagef("invalid ticket, repository or rounds until ok on row %d", rowNumber)
		}
		at, err := time.Parse(time.RFC3339Nano, row[2])
		if err != nil {
			at, err = time.Parse("2006-01-02", row[2])
		}
		if err != nil || at.Year() < 1970 || at.After(time.Now()) {
			return nil, usagef("invalid first look on row %d; use RFC3339 or YYYY-MM-DD (UTC)", rowNumber)
		}
		rules := []string{}
		for _, rule := range importRuleIDs.FindAllString(row[4], -1) {
			if len(rules) == 0 || !containsRule(rules, rule) {
				rules = append(rules, rule)
			}
		}
		if len(rules) > 32 {
			return nil, usagef("too many cited rules on row %d", rowNumber)
		}
		sort.Strings(rules)
		for round := 1; round <= n; round++ {
			// The TSV says which rules occurred for the ticket, not in which
			// round. Cite them once on the first look rather than multiplying them.
			cited := []string{}
			if round == 1 {
				cited = rules
			}
			verdict := "changes"
			if round == n {
				verdict = "ok"
			}
			in := reviewRoundWire{Ticket: row[0], Kind: "design", Round: round, Verdict: verdict, Rules: cited, At: at.UTC().Format(time.RFC3339Nano), Role: "design_reviewer", Artifact: row[1] + ":" + row[0], Source: "imported"}
			identity := row[0] + ":" + strconv.Itoa(round)
			encoded, _ := json.Marshal(in)
			if old, exists := seen[identity]; exists {
				if old != string(encoded) {
					return nil, usagef("conflicting duplicate ticket on row %d", rowNumber)
				}
				continue
			}
			seen[identity] = string(encoded)
			plan = append(plan, in)
			if len(plan) > 10000 {
				return nil, usagef("review TSV exceeds 10000 round outcomes")
			}
		}
	}
	return plan, nil
}

func containsRule(rules []string, rule string) bool {
	for _, candidate := range rules {
		if candidate == rule {
			return true
		}
	}
	return false
}
