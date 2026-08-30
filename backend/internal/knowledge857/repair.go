// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package knowledge857 implements the one-shot, clone-only repair which
// removes byte-for-byte-semantic duplicate knowledge rows before M162.
package knowledge857

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/inspr-at/paimos/backend/db" // register PAIMOS SQLite functions and connection hooks
)

const (
	databaseName = "ppm.db"
	walName      = "ppm.db-wal"
	shmName      = "ppm.db-shm"
	reportFormat = "pai-857-offline-repair/v1"
)

var trioNames = []string{databaseName, shmName, walName}

// Policy is the immutable authorization envelope for one backup.
type Policy struct {
	SourceBasename string
	Files          map[string]Fingerprint
	IssueCount     int64
	KnowledgeCount int64
}

// Fingerprint locks both a backup member's size and digest.
type Fingerprint struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ProductionPolicy is deliberately not configurable from CLI flags.
var ProductionPolicy = Policy{
	SourceBasename: "ppm-857-20260830-2130",
	Files: map[string]Fingerprint{
		databaseName: {Size: 196284416, SHA256: "8531f27c41ce8824e737f35a909ae867a3f5247002dd62fcfcd6bef3d2521033"},
		walName:      {Size: 10102272, SHA256: "cf8e76a8de55c34181ec33bfcb54bcfca72e92fb1e60813f18b2581474c80066"},
		shmName:      {Size: 32768, SHA256: "554c72524009b4159b0016962e4e53db6e938483b1d2c4aee4872f23d5134150"},
	},
	IssueCount:     4881,
	KnowledgeCount: 326,
}

type Options struct {
	SourceBackupDir string
	CloneDir        string
	ReportPath      string
	Policy          Policy
	// AfterCopy is test-only fault injection. Production callers leave it nil.
	AfterCopy func() error
	// AfterDelete is test-only fault injection. Production callers leave it nil.
	AfterDelete func(deleted int) error
}

type Identity struct {
	Scope      string  `json:"scope"`
	OwnerID    *int64  `json:"owner_id"`
	Type       string  `json:"type"`
	Slug       string  `json:"slug"`
	SurvivorID int64   `json:"survivor_id"`
	LoserIDs   []int64 `json:"loser_ids"`
}

type Report struct {
	Format            string                 `json:"format"`
	Status            string                 `json:"status"`
	ErrorCode         string                 `json:"error_code,omitempty"`
	ErrorDetail       string                 `json:"error_detail,omitempty"`
	SourceBasename    string                 `json:"source_basename"`
	ClonePath         string                 `json:"clone_path,omitempty"`
	SourceBefore      map[string]Fingerprint `json:"source_before,omitempty"`
	SourceAfter       map[string]Fingerprint `json:"source_after,omitempty"`
	CloneBefore       map[string]Fingerprint `json:"clone_before,omitempty"`
	IssueCountBefore  int64                  `json:"issue_count_before,omitempty"`
	IssueCountAfter   int64                  `json:"issue_count_after,omitempty"`
	KnowledgeBefore   int64                  `json:"knowledge_count_before,omitempty"`
	KnowledgeAfter    int64                  `json:"knowledge_count_after,omitempty"`
	DuplicateGroups   []Identity             `json:"duplicate_groups,omitempty"`
	ReferenceSurfaces []string               `json:"reference_surfaces_checked,omitempty"`
	DeletedRows       int                    `json:"deleted_rows,omitempty"`
	IntegrityCheck    string                 `json:"integrity_check,omitempty"`
	ForeignKeyCheck   string                 `json:"foreign_key_check,omitempty"`
	Migration162State string                 `json:"migration_162_state,omitempty"`
	ReportSHA256      string                 `json:"report_sha256"`
}

type codedError struct{ code, detail string }

func (e *codedError) Error() string { return e.code + ": " + e.detail }
func refuse(code, format string, args ...any) error {
	return &codedError{code: code, detail: fmt.Sprintf(format, args...)}
}

// Run validates the locked source as bytes, creates a new clone, and opens only
// that clone with SQLite. A report is written exactly once at the explicit path.
func Run(ctx context.Context, opts Options) (Report, error) {
	report := Report{Format: reportFormat, Status: "refused", SourceBasename: opts.Policy.SourceBasename}
	if opts.Policy.SourceBasename == "" {
		opts.Policy = ProductionPolicy
		report.SourceBasename = opts.Policy.SourceBasename
	}
	if err := validateReportPath(opts); err != nil {
		return report, err
	}
	err := run(ctx, opts, &report)
	if err != nil {
		var coded *codedError
		if errors.As(err, &coded) {
			report.ErrorCode, report.ErrorDetail = coded.code, coded.detail
		} else {
			report.ErrorCode, report.ErrorDetail = "internal", err.Error()
		}
	}
	if writeErr := writeReport(opts.ReportPath, &report); writeErr != nil {
		if err == nil {
			return report, writeErr
		}
		return report, fmt.Errorf("%w; write report: %v", err, writeErr)
	}
	return report, err
}

func run(ctx context.Context, opts Options, report *Report) error {
	source, clone, err := validatePaths(opts)
	if err != nil {
		return err
	}
	report.ClonePath = clone
	before, err := fingerprintDirectory(source)
	if err != nil {
		return err
	}
	report.SourceBefore = before
	if err := matchPolicy(before, opts.Policy.Files); err != nil {
		return err
	}
	if err := copyTrio(source, clone); err != nil {
		return err
	}
	cloneBefore, err := fingerprintDirectory(clone)
	if err != nil {
		return err
	}
	report.CloneBefore = cloneBefore
	if err := sameFingerprints(before, cloneBefore, "clone_copy_mismatch"); err != nil {
		return err
	}
	if opts.AfterCopy != nil {
		if hookErr := opts.AfterCopy(); hookErr != nil {
			return refuse("injected_failure", "%v", hookErr)
		}
	}
	afterCopy, err := fingerprintDirectory(source)
	if err != nil {
		return err
	}
	if err := sameFingerprints(before, afterCopy, "source_changed"); err != nil {
		return err
	}

	database, err := openClone(clone)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := preflight(ctx, database, opts.Policy, report); err != nil {
		return err
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return refuse("transaction_begin", "%v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			report.DeletedRows = 0
			report.IssueCountAfter = 0
			report.KnowledgeAfter = 0
		}
	}()

	plan, semanticColumns, err := buildPlan(ctx, tx)
	if err != nil {
		return err
	}
	report.DuplicateGroups = plan
	surfaces, err := proveUnreferenced(ctx, tx, plan)
	report.ReferenceSurfaces = surfaces
	if err != nil {
		return err
	}
	for _, group := range plan {
		for _, loser := range group.LoserIDs {
			query, args := guardedDelete(group, loser, semanticColumns)
			result, execErr := tx.ExecContext(ctx, query, args...)
			if execErr != nil {
				return refuse("guarded_delete", "loser id=%d: %v", loser, execErr)
			}
			rows, rowsErr := result.RowsAffected()
			if rowsErr != nil || rows != 1 {
				return refuse("guarded_delete", "loser id=%d rows=%d err=%v", loser, rows, rowsErr)
			}
			report.DeletedRows++
			if opts.AfterDelete != nil {
				if hookErr := opts.AfterDelete(report.DeletedRows); hookErr != nil {
					return refuse("injected_failure", "%v", hookErr)
				}
			}
		}
	}
	if err := postflightTx(ctx, tx, opts.Policy, report); err != nil {
		return err
	}
	preCommitSource, err := fingerprintDirectory(source)
	if err != nil {
		return err
	}
	if err := sameFingerprints(before, preCommitSource, "source_changed"); err != nil {
		return err
	}
	report.SourceAfter = preCommitSource
	if err := tx.Commit(); err != nil {
		return refuse("transaction_commit", "%v", err)
	}
	committed = true
	report.Status = "clean"
	return nil
}

func validateReportPath(opts Options) error {
	if opts.ReportPath == "" || !filepath.IsAbs(opts.ReportPath) {
		return refuse("invalid_report_path", "report path must be explicit and absolute")
	}
	if info, err := os.Lstat(opts.ReportPath); err == nil {
		return refuse("report_exists", "report path already exists (%s)", info.Name())
	} else if !errors.Is(err, os.ErrNotExist) {
		return refuse("invalid_report_path", "%v", err)
	}
	if err := realParent(filepath.Clean(opts.ReportPath)); err != nil {
		return refuse("invalid_report_path", "%v", err)
	}
	return nil
}

func realParent(path string) error {
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	if canonical != parent {
		return fmt.Errorf("parent path contains a symlink")
	}
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("parent is not a directory")
	}
	return nil
}

func validatePaths(opts Options) (string, string, error) {
	if opts.SourceBackupDir == "" || !filepath.IsAbs(opts.SourceBackupDir) || opts.CloneDir == "" || !filepath.IsAbs(opts.CloneDir) {
		return "", "", refuse("invalid_path", "source and clone paths must be explicit and absolute")
	}
	source := filepath.Clean(opts.SourceBackupDir)
	clone := filepath.Clean(opts.CloneDir)
	if source == clone {
		return "", "", refuse("same_path", "source and clone paths are identical")
	}
	if filepath.Base(source) != opts.Policy.SourceBasename {
		return "", "", refuse("source_basename", "got %q want %q", filepath.Base(source), opts.Policy.SourceBasename)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", "", refuse("source_missing", "%v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", refuse("unsafe_source", "source must be a real directory, not a symlink")
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", refuse("unsafe_source", "%v", err)
	}
	if canonicalSource != source || filepath.Base(canonicalSource) != opts.Policy.SourceBasename {
		return "", "", refuse("unsafe_source", "source path contains a symlink or canonical basename mismatch")
	}
	if _, err := os.Lstat(clone); err == nil {
		return "", "", refuse("clone_exists", "clone destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", refuse("invalid_clone", "%v", err)
	}
	if err := realParent(clone); err != nil {
		return "", "", refuse("invalid_clone", "%v", err)
	}
	if within(source, clone) || within(clone, source) {
		return "", "", refuse("overlapping_paths", "source and clone must not contain one another")
	}
	reportPath := filepath.Clean(opts.ReportPath)
	if reportPath == clone || within(source, reportPath) || within(clone, reportPath) {
		return "", "", refuse("unsafe_report_path", "report must be outside source and clone")
	}
	return source, clone, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func fingerprintDirectory(dir string) (map[string]Fingerprint, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, refuse("read_backup", "%v", err)
	}
	if len(entries) != len(trioNames) {
		return nil, refuse("unsafe_backup_shape", "expected exactly three files, got %d", len(entries))
	}
	want := map[string]bool{}
	for _, name := range trioNames {
		want[name] = true
	}
	result := make(map[string]Fingerprint, len(entries))
	for _, entry := range entries {
		if !want[entry.Name()] {
			return nil, refuse("unsafe_backup_shape", "unexpected member %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return nil, refuse("read_backup", "%s: %v", entry.Name(), err)
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, refuse("unsafe_backup_shape", "%q is not a regular non-symlink file", entry.Name())
		}
		path := filepath.Join(dir, entry.Name())
		f, err := os.Open(path) // #nosec G304 -- dir is a validated canonical source/new clone and entry.Name is one of the exact locked trio names.
		if err != nil {
			return nil, refuse("read_backup", "%s: %v", entry.Name(), err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return nil, refuse("read_backup", "%s: copy=%v close=%v", entry.Name(), copyErr, closeErr)
		}
		result[entry.Name()] = Fingerprint{Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	return result, nil
}

func matchPolicy(got, want map[string]Fingerprint) error {
	if len(got) != len(want) {
		return refuse("fingerprint_mismatch", "file count mismatch")
	}
	for _, name := range trioNames {
		if got[name] != want[name] {
			return refuse("fingerprint_mismatch", "%s got=%+v want=%+v", name, got[name], want[name])
		}
	}
	return nil
}

func sameFingerprints(left, right map[string]Fingerprint, code string) error {
	if len(left) != len(right) {
		return refuse(code, "file count changed")
	}
	for _, name := range trioNames {
		if left[name] != right[name] {
			return refuse(code, "%s changed", name)
		}
	}
	return nil
}

func copyTrio(source, clone string) error {
	if err := os.Mkdir(clone, 0700); err != nil {
		return refuse("create_clone", "%v", err)
	}
	for _, name := range trioNames {
		in, err := os.Open(filepath.Join(source, name)) // #nosec G304 -- source is canonical and name comes only from the fixed trioNames allowlist.
		if err != nil {
			return refuse("copy_clone", "%s: %v", name, err)
		}
		out, err := os.OpenFile(filepath.Join(clone, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- clone is a newly created validated directory and name is a fixed trio member.
		if err != nil {
			in.Close()
			return refuse("copy_clone", "%s: %v", name, err)
		}
		_, copyErr := io.Copy(out, in)
		syncErr := out.Sync()
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil || syncErr != nil || closeOutErr != nil || closeInErr != nil {
			return refuse("copy_clone", "%s copy=%v sync=%v close_out=%v close_in=%v", name, copyErr, syncErr, closeOutErr, closeInErr)
		}
	}
	return nil
}

func openClone(clone string) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(clone, databaseName)) + "?_txlock=immediate"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, refuse("clone_open", "%v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		if strings.Contains(strings.ToLower(err.Error()), "malformed") || strings.Contains(strings.ToLower(err.Error()), "disk image") {
			return nil, refuse("integrity_check", "clone rejected as malformed while opening: %v", err)
		}
		return nil, refuse("clone_open", "%v", err)
	}
	return database, nil
}

var expectedIssueColumns = []string{
	"id", "project_id", "issue_number", "type", "title", "description", "acceptance_criteria", "notes", "status", "priority",
	"billing_type", "total_budget", "rate_hourly", "rate_lp", "start_date", "end_date", "group_state", "sprint_state", "jira_id",
	"jira_version", "jira_text", "estimate_hours", "estimate_lp", "ar_hours", "ar_lp", "time_override", "color", "archived",
	"assignee_id", "created_by", "accepted_at", "accepted_by", "invoiced_at", "invoice_number", "created_at", "updated_at", "target_ar",
	"deleted_at", "deleted_by", "slug", "category_metadata", "user_id", "reference_count", "last_referenced_at", "report_summary",
	"content_rev", "content_revised_at", "deps_reviewed_at", "pharos_request_id",
}

var excludedSemantic = map[string]bool{
	"id": true, "project_id": true, "user_id": true, "issue_number": true, "type": true, "slug": true,
	"created_at": true, "updated_at": true, "last_referenced_at": true, "content_rev": true,
	"content_revised_at": true, "deps_reviewed_at": true,
}

func preflight(ctx context.Context, database *sql.DB, policy Policy, report *Report) error {
	if err := checkOneRow(ctx, database, "PRAGMA integrity_check", "ok", "integrity_check"); err != nil {
		return err
	}
	clean, err := queryEmpty(ctx, database, "PRAGMA foreign_key_check")
	if err != nil {
		return refuse("foreign_key_check", "%v", err)
	}
	if !clean {
		return refuse("foreign_key_check", "violations found")
	}
	if err := exactIssueSchema(ctx, database); err != nil {
		return err
	}
	var m162 int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_versions WHERE version=162`).Scan(&m162); err != nil {
		return refuse("migration_state", "%v", err)
	}
	if m162 != 0 {
		return refuse("migration_162_applied", "M162 must be pending")
	}
	var issues, knowledge int64
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*),coalesce(sum(CASE WHEN type IN ('memory','runbook','external_system','related_project','guideline') THEN 1 ELSE 0 END),0) FROM issues`).Scan(&issues, &knowledge); err != nil {
		return refuse("count_check", "%v", err)
	}
	if issues != policy.IssueCount || knowledge != policy.KnowledgeCount {
		return refuse("count_mismatch", "issues=%d/%d knowledge=%d/%d", issues, policy.IssueCount, knowledge, policy.KnowledgeCount)
	}
	report.IssueCountBefore, report.KnowledgeBefore = issues, knowledge
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func exactIssueSchema(ctx context.Context, q queryer) error {
	columns, err := tableColumns(ctx, q, "issues")
	if err != nil {
		return refuse("schema_drift", "%v", err)
	}
	want := append([]string(nil), expectedIssueColumns...)
	sort.Strings(columns)
	sort.Strings(want)
	if strings.Join(columns, "\x00") != strings.Join(want, "\x00") {
		return refuse("schema_drift", "issues columns got=%v want=%v", columns, want)
	}
	return nil
}

func tableColumns(ctx context.Context, q queryer, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}
	return columns, rows.Err()
}

func checkOneRow(ctx context.Context, q queryer, query, want, code string) error {
	var got string
	if err := q.QueryRowContext(ctx, query).Scan(&got); err != nil {
		return refuse(code, "%v", err)
	}
	if got != want {
		return refuse(code, "got %q want %q", got, want)
	}
	return nil
}

func queryEmpty(ctx context.Context, q queryer, query string, args ...any) (bool, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return false, nil
	}
	return true, rows.Err()
}

func buildPlan(ctx context.Context, tx *sql.Tx) ([]Identity, []string, error) {
	if err := exactIssueSchema(ctx, tx); err != nil {
		return nil, nil, err
	}
	var invalidID int64
	var invalidProject, invalidUser sql.NullInt64
	var invalidType string
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,user_id,type FROM issues
		WHERE (project_id IS NOT NULL AND user_id IS NOT NULL) OR (user_id IS NOT NULL AND type<>'memory')
		ORDER BY id LIMIT 1`).Scan(&invalidID, &invalidProject, &invalidUser, &invalidType)
	if err == nil {
		if invalidProject.Valid && invalidUser.Valid {
			return nil, nil, refuse("invalid_ownership", "id=%d project_id=%v user_id=%v type=%q", invalidID, invalidProject, invalidUser, invalidType)
		}
		return nil, nil, refuse("unsupported_user_type", "id=%d user_id=%d type=%q", invalidID, invalidUser.Int64, invalidType)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, refuse("scan_issues", "invalid ownership scan: %v", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,user_id,type,slug,deleted_at FROM issues
		WHERE slug IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return nil, nil, refuse("scan_issues", "%v", err)
	}
	type item struct {
		id            int64
		project, user sql.NullInt64
		typ, slug     string
		deleted       sql.NullString
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.project, &it.user, &it.typ, &it.slug, &it.deleted); err != nil {
			rows.Close()
			return nil, nil, refuse("scan_issues", "%v", err)
		}
		items = append(items, it)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, refuse("scan_issues", "%v", err)
	}
	groups := map[string][]item{}
	for _, it := range items {
		owner := int64(0)
		scope := "instance"
		if it.project.Valid {
			scope = "project"
			owner = it.project.Int64
		} else if it.user.Valid {
			scope = "user"
			owner = it.user.Int64
		}
		key := fmt.Sprintf("%s\x00%d\x00%s\x00%s", scope, owner, it.typ, it.slug)
		groups[key] = append(groups[key], it)
	}
	semantic := make([]string, 0, len(expectedIssueColumns))
	for _, col := range expectedIssueColumns {
		if !excludedSemantic[col] {
			semantic = append(semantic, col)
		}
	}
	var plan []Identity
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		parts := strings.Split(key, "\x00")
		if !isKnowledgeType(group[0].typ) {
			return nil, nil, refuse("nonknowledge_collision", "scope=%s type=%q slug=%q ids include %d", parts[0], group[0].typ, group[0].slug, group[0].id)
		}
		for _, it := range group {
			if it.deleted.Valid {
				return nil, nil, refuse("deleted_collision", "identity includes deleted id=%d", it.id)
			}
		}
		owner := group[0].project
		if parts[0] == "user" {
			owner = group[0].user
		}
		var ownerPtr *int64
		if owner.Valid {
			v := owner.Int64
			ownerPtr = &v
		}
		identity := Identity{Scope: parts[0], OwnerID: ownerPtr, Type: group[0].typ, Slug: group[0].slug, SurvivorID: group[0].id}
		for _, loser := range group[1:] {
			identity.LoserIDs = append(identity.LoserIDs, loser.id)
		}
		equal, err := rowsSemanticallyEqual(ctx, tx, identity, semantic)
		if err != nil {
			return nil, nil, err
		}
		if !equal {
			return nil, nil, refuse("divergent_duplicate", "scope=%s type=%s slug=%s ids=%v", identity.Scope, identity.Type, identity.Slug, append([]int64{identity.SurvivorID}, identity.LoserIDs...))
		}
		plan = append(plan, identity)
	}
	if len(plan) == 0 {
		return nil, nil, refuse("no_authorized_duplicates", "no active exact duplicate identity groups")
	}
	return plan, semantic, nil
}

func isKnowledgeType(issueType string) bool {
	switch issueType {
	case "memory", "runbook", "external_system", "related_project", "guideline":
		return true
	default:
		return false
	}
}

func rowsSemanticallyEqual(ctx context.Context, tx *sql.Tx, group Identity, columns []string) (bool, error) {
	ids := append([]int64{group.SurvivorID}, group.LoserIDs...)
	// #nosec G202 -- columns is the closed expectedIssueColumns subset and placeholders emits only '?' tokens; every id is bound below.
	query := `SELECT ` + strings.Join(columns, ",") + ` FROM issues WHERE id IN (` + placeholders(len(ids)) + `) ORDER BY id`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return false, refuse("compare_duplicates", "%v", err)
	}
	defer rows.Close()
	var baseline []any
	count := 0
	for rows.Next() {
		vals := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return false, refuse("compare_duplicates", "%v", err)
		}
		if count == 0 {
			baseline = vals
		} else if !sqlValuesEqual(baseline, vals) {
			return false, nil
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return false, refuse("compare_duplicates", "%v", err)
	}
	return count == len(ids), nil
}

func sqlValuesEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if fmt.Sprintf("%T:%v", a[i], a[i]) != fmt.Sprintf("%T:%v", b[i], b[i]) {
			return false
		}
	}
	return true
}
func placeholders(n int) string {
	values := make([]string, n)
	for i := range values {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}

type refSurface struct{ table, column string }

// durableNonFKIssueReferences is closed against the exact M161 schema. These
// columns deliberately encode issue IDs without a direct FK to issues (some use
// composite delivery/control constraints). A new matching column is refused.
var durableNonFKIssueReferences = map[string]bool{
	"agent_mode_legacy_roots\x00issue_id":        true,
	"control_capability_grants\x00root_issue_id": true,
	"control_capability_leases\x00root_issue_id": true,
	"control_commands\x00root_issue_id":          true,
	"control_events\x00root_issue_id":            true,
	"control_input_requests\x00root_issue_id":    true,
	"control_runtime_states\x00root_issue_id":    true,
	"delivery_agent_run_links\x00root_issue_id":  true,
	"delivery_change_log\x00root_issue_id":       true,
	"delivery_evidence\x00root_issue_id":         true,
	"delivery_stage_durations\x00root_issue_id":  true,
	"external_stage_handoffs\x00root_issue_id":   true,
}

func proveUnreferenced(ctx context.Context, tx *sql.Tx, plan []Identity) ([]string, error) {
	surfaces, err := referenceSurfaces(ctx, tx)
	if err != nil {
		return nil, err
	}
	checked := make([]string, 0, len(surfaces)+4)
	for _, surface := range surfaces {
		checked = append(checked, surface.table+"."+surface.column)
	}
	checked = append(checked, "issue_control_revisions.issue_id (mandatory projection)", "mutation_log.subject_type+subject_id", "entity_embeddings.entity_type+entity_id", "entity_relations.source_type+source_id", "entity_relations.target_type+target_id")
	sort.Strings(checked)
	for _, group := range plan {
		for _, issueID := range append([]int64{group.SurvivorID}, group.LoserIDs...) {
			var projections int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_control_revisions WHERE issue_id=?`, issueID).Scan(&projections); err != nil {
				return checked, refuse("reference_scan", "issue_control_revisions: %v", err)
			}
			if projections != 1 {
				return checked, refuse("control_projection", "id=%d rows=%d want=1", issueID, projections)
			}
		}
		for _, loser := range group.LoserIDs {
			for _, surface := range surfaces {
				var count int
				// #nosec G201 -- identifiers are quoted names discovered from the fingerprint-locked exact SQLite schema and checked against FK/closed non-FK rules.
				query := fmt.Sprintf(`SELECT COUNT(*) FROM %q WHERE %q=?`, surface.table, surface.column)
				if err := tx.QueryRowContext(ctx, query, loser).Scan(&count); err != nil {
					return checked, refuse("reference_scan", "%s.%s: %v", surface.table, surface.column, err)
				}
				if count > 0 {
					return checked, refuse("referenced_duplicate", "id=%d surface=%s.%s count=%d", loser, surface.table, surface.column, count)
				}
			}
			for _, typed := range []struct{ table, typeCol, idCol string }{
				{"mutation_log", "subject_type", "subject_id"}, {"entity_embeddings", "entity_type", "entity_id"}, {"entity_relations", "source_type", "source_id"}, {"entity_relations", "target_type", "target_id"},
			} {
				var count int
				// #nosec G201 -- table and column identifiers are package-local constants in the closed typed-reference list above.
				query := fmt.Sprintf(`SELECT COUNT(*) FROM %q WHERE %q='issue' AND %q=?`, typed.table, typed.typeCol, typed.idCol)
				if err := tx.QueryRowContext(ctx, query, loser).Scan(&count); err != nil {
					return checked, refuse("reference_scan", "%s: %v", typed.table, err)
				}
				if count > 0 {
					return checked, refuse("referenced_duplicate", "id=%d typed_surface=%s.%s", loser, typed.table, typed.idCol)
				}
			}
		}
	}
	return checked, nil
}

func referenceSurfaces(ctx context.Context, tx *sql.Tx) ([]refSurface, error) {
	tableRows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, refuse("reference_schema", "%v", err)
	}
	var tables []string
	for tableRows.Next() {
		var table string
		if err := tableRows.Scan(&table); err != nil {
			tableRows.Close()
			return nil, refuse("reference_schema", "%v", err)
		}
		tables = append(tables, table)
	}
	if err := tableRows.Close(); err != nil {
		return nil, refuse("reference_schema", "%v", err)
	}
	fk := map[string]bool{}
	var surfaces []refSurface
	for _, table := range tables {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
		if err != nil {
			return nil, refuse("reference_schema", "%s: %v", table, err)
		}
		for rows.Next() {
			var id, seq int
			var target, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				rows.Close()
				return nil, refuse("reference_schema", "%v", err)
			}
			if target == "issues" {
				key := table + "\x00" + from
				fk[key] = true
				surfaces = append(surfaces, refSurface{table, from})
			}
		}
		if err := rows.Close(); err != nil {
			return nil, refuse("reference_schema", "%v", err)
		}
	}
	allowedProjection := map[string]bool{"issue_control_revisions\x00issue_id": true}
	var unknown []string
	for _, table := range tables {
		cols, err := tableColumns(ctx, tx, table)
		if err != nil {
			return nil, refuse("reference_schema", "%v", err)
		}
		for _, col := range cols {
			shaped := col == "issue_id" || col == "root_issue_id" || col == "created_issue_id" || strings.HasSuffix(col, "_issue_id")
			if !shaped {
				continue
			}
			key := table + "\x00" + col
			if fk[key] {
				continue
			}
			if allowedProjection[key] {
				continue
			}
			if durableNonFKIssueReferences[key] {
				surfaces = append(surfaces, refSurface{table, col})
				continue
			}
			unknown = append(unknown, table+"."+col)
		}
	}
	if len(unknown) > 0 {
		return nil, refuse("unknown_reference_surface", "%s", strings.Join(unknown, ","))
	}
	sort.Slice(surfaces, func(i, j int) bool {
		if surfaces[i].table == surfaces[j].table {
			return surfaces[i].column < surfaces[j].column
		}
		return surfaces[i].table < surfaces[j].table
	})
	return surfaces, nil
}

func guardedDelete(group Identity, loser int64, semantic []string) (string, []any) {
	conditions := []string{"candidate.id=?", "candidate.deleted_at IS NULL", "survivor.id=?", "survivor.deleted_at IS NULL", "candidate.type IS survivor.type", "candidate.slug IS survivor.slug", "candidate.project_id IS survivor.project_id", "candidate.user_id IS survivor.user_id"}
	for _, col := range semantic {
		conditions = append(conditions, fmt.Sprintf(`candidate.%q IS survivor.%q`, col, col))
	}
	query := `DELETE FROM issues WHERE id=? AND EXISTS(SELECT 1 FROM issues candidate JOIN issues survivor ON survivor.id=? WHERE ` + strings.Join(conditions, " AND ") + `)`
	return query, []any{loser, group.SurvivorID, loser, group.SurvivorID}
}

func postflightTx(ctx context.Context, tx *sql.Tx, policy Policy, report *Report) error {
	var issues, knowledge int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),coalesce(sum(CASE WHEN type IN ('memory','runbook','external_system','related_project','guideline') THEN 1 ELSE 0 END),0) FROM issues`).Scan(&issues, &knowledge); err != nil {
		return refuse("post_count", "%v", err)
	}
	if issues != policy.IssueCount-int64(report.DeletedRows) {
		return refuse("post_count", "got=%d want=%d", issues, policy.IssueCount-int64(report.DeletedRows))
	}
	report.IssueCountAfter = issues
	if knowledge != policy.KnowledgeCount-int64(report.DeletedRows) {
		return refuse("post_count", "knowledge got=%d want=%d", knowledge, policy.KnowledgeCount-int64(report.DeletedRows))
	}
	report.KnowledgeAfter = knowledge
	var dups int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM issues WHERE deleted_at IS NULL AND slug IS NOT NULL GROUP BY project_id,user_id,type,slug HAVING COUNT(*)>1)`).Scan(&dups); err != nil {
		return refuse("post_duplicates", "%v", err)
	}
	if dups != 0 {
		return refuse("post_duplicates", "remaining=%d", dups)
	}
	clean, err := queryEmpty(ctx, tx, "PRAGMA foreign_key_check")
	if err != nil || !clean {
		return refuse("foreign_key_check", "clean=%v err=%v", clean, err)
	}
	if err := checkOneRow(ctx, tx, "PRAGMA integrity_check", "ok", "integrity_check"); err != nil {
		return err
	}
	report.IntegrityCheck = "ok"
	report.ForeignKeyCheck = "ok"
	var m162 int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_versions WHERE version=162`).Scan(&m162); err != nil || m162 != 0 {
		return refuse("migration_state", "M162 count=%d err=%v", m162, err)
	}
	report.Migration162State = "pending"
	return nil
}

func writeReport(path string, report *Report) error {
	report.ReportSHA256 = ""
	canonical, err := json.Marshal(report)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	report.ReportSHA256 = hex.EncodeToString(digest[:])
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- path is explicit absolute output validated as new beneath a real non-symlink parent.
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// VerifyReportDigest recomputes the self-independent report digest.
func VerifyReportDigest(report Report) bool {
	claimed := report.ReportSHA256
	report.ReportSHA256 = ""
	payload, err := json.Marshal(report)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(payload)
	return claimed == hex.EncodeToString(sum[:])
}
