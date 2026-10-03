// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/workqueue"
)

type apiNode struct {
	Queued    *workqueue.Queued `json:"queued,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
	ID        string            `json:"id"`
	Key       string            `json:"key"`
	KindID    string            `json:"kind_id"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Fields    json.RawMessage   `json:"fields"`
	State     string            `json:"state"`
	ParentID  *string           `json:"parent_id"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type nodePage struct {
	Items      []apiNode `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

type apiKind struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	Label       string          `json:"label"`
	ShortPrefix string          `json:"short_prefix"`
	Icon        string          `json:"icon,omitempty"`
	FieldSchema json.RawMessage `json:"field_schema,omitempty"`
}

// issueFamilyKind matches the server and the web convert sheet. An explicit
// issue_family boolean wins. Otherwise the seeded issue names, and a kind that
// still uses one of those icons, are the family.
func issueFamilyKind(kind apiKind) bool {
	if marked, ok := explicitIssueFamily(kind.FieldSchema); ok {
		return marked
	}
	return seededIssueName(kind.Slug) || seededIssueName(kind.Icon)
}

func explicitIssueFamily(raw json.RawMessage) (marked, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false, false
	}
	value, exists := obj["issue_family"]
	if !exists {
		return false, false
	}
	flag, isBool := value.(bool)
	if !isBool {
		return false, false
	}
	return flag, true
}

func seededIssueName(name string) bool {
	switch name {
	case "epic", "ticket", "task":
		return true
	default:
		return false
	}
}

type kindPage struct {
	Items []apiKind `json:"items"`
}

type kindTable struct {
	bySlug map[string]apiKind
	byID   map[string]apiKind
}

func (t kindTable) slug(id string) string {
	if k, ok := t.byID[id]; ok {
		return k.Slug
	}
	return ""
}

func (rt *runtime) api() (*client.Client, error) {
	inst, err := rt.resolve()
	if err != nil {
		return nil, err
	}
	c := client.New(inst.URL, inst.APIKey)
	c.ConfirmStepUp = localStepUp(inst.URL, inst.AgentdStateRoot)
	return c, nil
}

func (rt *runtime) do(method, path string, body, dest any) error {
	return rt.doHeaders(method, path, body, dest, nil)
}

func (rt *runtime) doCtx(ctx context.Context, method, path string, body, dest any) error {
	return rt.doHeadersCtx(ctx, method, path, body, dest, nil)
}

func (rt *runtime) doHeaders(method, path string, body, dest any, headers map[string]string) error {
	return rt.doHeadersCtx(context.Background(), method, path, body, dest, headers)
}

func (rt *runtime) doHeadersCtx(ctx context.Context, method, path string, body, dest any, headers map[string]string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := rt.api()
	if err != nil {
		return err
	}
	if err := c.DoWithHeaders(ctx, method, path, body, dest, headers); err != nil {
		return rt.fail(err, c.Token)
	}
	return nil
}

func (rt *runtime) ticketWebURL(key string) string {
	base := ""
	if inst, err := rt.resolve(); err == nil {
		base = strings.TrimRight(inst.URL, "/")
	}
	prefix := keyPrefix(key)
	if prefix == "" {
		return base + "/p/" + url.PathEscape(key)
	}
	return base + "/p/" + url.PathEscape(prefix) + "/" + url.PathEscape(key)
}

func (rt *runtime) loadKinds() (kindTable, error) {
	return rt.loadKindsCtx(context.Background())
}

func (rt *runtime) loadKindsCtx(ctx context.Context) (kindTable, error) {
	if err := ctx.Err(); err != nil {
		return kindTable{}, err
	}
	if rt.kinds != nil {
		return *rt.kinds, nil
	}
	var page kindPage
	if err := rt.doCtx(ctx, http.MethodGet, "/api/kinds", nil, &page); err != nil {
		return kindTable{}, err
	}
	table := kindTable{bySlug: map[string]apiKind{}, byID: map[string]apiKind{}}
	for _, k := range page.Items {
		table.bySlug[k.Slug] = k
		table.byID[k.ID] = k
	}
	rt.kinds = &table
	return table, nil
}

func (rt *runtime) kind(slug string) (apiKind, error) {
	return rt.kindCtx(context.Background(), slug)
}

func (rt *runtime) kindCtx(ctx context.Context, slug string) (apiKind, error) {
	table, err := rt.loadKindsCtx(ctx)
	if err != nil {
		return apiKind{}, err
	}
	k, ok := table.bySlug[slug]
	if !ok {
		return apiKind{}, rt.fail(fmt.Errorf("node kind %q is not configured", slug), "")
	}
	return k, nil
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, vs := range q {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func (rt *runtime) walkNodes(q url.Values, stop func(apiNode) bool) ([]apiNode, error) {
	return rt.walkNodesCtx(context.Background(), q, stop)
}

func (rt *runtime) walkNodesCtx(ctx context.Context, q url.Values, stop func(apiNode) bool) ([]apiNode, error) {
	q = cloneValues(q)
	if q.Get("limit") == "" {
		q.Set("limit", "200")
	}
	var all []apiNode
	for page := 0; page < 50; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := "/api/nodes"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}
		var body nodePage
		if err := rt.doCtx(ctx, http.MethodGet, path, nil, &body); err != nil {
			return nil, err
		}
		for _, n := range body.Items {
			all = append(all, n)
			if stop != nil && stop(n) {
				return all, nil
			}
		}
		if body.NextCursor == nil || strings.TrimSpace(*body.NextCursor) == "" {
			break
		}
		q.Set("cursor", *body.NextCursor)
	}
	return all, nil
}

func (rt *runtime) nodeByKey(key string) (apiNode, error) {
	key = strings.TrimSpace(key)
	var found *apiNode
	// q matches keys first (exact and prefix), so the key's node is in the
	// first page instead of a walk over every node in the tenant.
	_, err := rt.walkNodes(url.Values{"q": {key}}, func(n apiNode) bool {
		if n.Key == key {
			found = &n
			return true
		}
		return false
	})
	if err != nil {
		return apiNode{}, err
	}
	if found == nil {
		var resolved apiNode
		if err := rt.do(http.MethodGet, "/api/node-keys/"+url.PathEscape(key), nil, &resolved); err != nil {
			return apiNode{}, rt.fail(fmt.Errorf("issue %q not found", key), "")
		}
		return resolved, nil
	}
	return *found, nil
}

func keyPrefix(key string) string {
	prefix, _, ok := strings.Cut(key, "-")
	if !ok {
		return ""
	}
	return prefix
}

func (rt *runtime) projectNode(ref string) (apiNode, error) {
	return rt.projectNodeCtx(context.Background(), ref)
}

func (rt *runtime) projectNodeCtx(ctx context.Context, ref string) (apiNode, error) {
	if err := ctx.Err(); err != nil {
		return apiNode{}, err
	}
	ref = strings.TrimSpace(ref)
	kind, err := rt.kindCtx(ctx, "project")
	if err != nil {
		return apiNode{}, err
	}
	nodes, err := rt.walkNodesCtx(ctx, url.Values{"kind_id": {kind.ID}}, nil)
	if err != nil {
		return apiNode{}, err
	}
	var hits []apiNode
	for _, n := range nodes {
		fields := fieldMap(n.Fields)
		classic, _ := fields["classic"].(map[string]any)
		classicKey, _ := classic["key"].(string)
		if n.Key == ref || fieldString(fields, "project_key") == ref || keyPrefix(n.Key) == ref || (classicKey != "" && strings.EqualFold(classicKey, ref)) {
			hits = append(hits, n)
		}
	}
	if len(hits) == 0 {
		return apiNode{}, rt.fail(fmt.Errorf("project key %q not found", ref), "")
	}
	if len(hits) > 1 {
		return apiNode{}, rt.fail(fmt.Errorf("project key %q is ambiguous", ref), "")
	}
	return hits[0], nil
}

func fieldMap(raw json.RawMessage) map[string]any {
	if len(bytesTrim(raw)) == 0 || string(raw) == "null" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func bytesTrim(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func fieldString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func fieldStrings(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []string:
		return v
	case []any:
		var out []string
		for _, item := range v {
			s, ok := item.(string)
			if ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func (rt *runtime) readText(inline, file, name string) (string, error) {
	if err := exclusive(inline, file, name); err != nil {
		return "", err
	}
	file = strings.TrimSpace(file)
	if file == "" {
		return inline, nil
	}
	if file == "-" {
		raw, err := io.ReadAll(io.LimitReader(rt.stdin, 1<<20))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		if len(raw) == 1<<20 {
			return "", usagef("--%s-file is too long", name)
		}
		return string(raw), nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	if len(raw) > 1<<20 {
		return "", usagef("--%s-file is too long", name)
	}
	return string(raw), nil
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}

func clipRunes(s string, n int, ellipsis string) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ellipsis
	}
	return string(r[:n]) + ellipsis
}
