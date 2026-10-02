// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

const goModule = "github.com/inspr-at/paimos"
const analysisRoot = "/workspace/source"

// GoAnalysisContext is fixed by the installed metadata image, not candidate
// GOFLAGS or go env. Unsupported targets/tags widen to full work. Toolchain and
// dependency pins cover the offline image's Go binary and module cache bytes.
type GoAnalysisContext struct {
	GOOS              string   `json:"goos"`
	GOARCH            string   `json:"goarch"`
	Tags              []string `json:"tags"`
	ToolchainDigest   string   `json:"toolchain_digest"`
	DependencyDigest  string   `json:"dependency_digest"`
	EnvironmentDigest string   `json:"environment_digest"`
}

// GoMetadataRecipe runs only in B's disposable guest, with its fixed offline
// environment (GOTOOLCHAIN=local, GOENV=off, GOWORK=off, GOPROXY=off). There is no
// host subprocess launcher here. Provision the absolute pinned Go binary in the
// image and override GOOS/GOARCH/CGO_ENABLED there, independently of candidate
// bytes. Missing images/cache/metadata mean a full shadow selection.
func GoMetadataRecipe(obligationID string) Recipe {
	return Recipe{ObligationID: obligationID, Stage: "metadata", Reporter: "command",
		Argv:     []string{"/opt/aeon/bin/go", "list", "-mod=readonly", "-deps", "-test", "-json", "./..."},
		Expected: []string{"command/" + obligationID}, OutputArtifact: "go-metadata.json"}
}

type goListModule struct {
	Path    string
	Main    bool
	Replace *json.RawMessage
}

type goListPackage struct {
	Dir, ImportPath, ForTest, Name                                                                 string
	Standard, Incomplete                                                                           bool
	Module                                                                                         *goListModule
	Error                                                                                          *json.RawMessage
	DepsErrors                                                                                     []json.RawMessage
	Imports, TestImports, XTestImports                                                             []string
	GoFiles, TestGoFiles, XTestGoFiles, IgnoredGoFiles                                             []string
	CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles []string
	EmbedFiles, TestEmbedFiles, XTestEmbedFiles                                                    []string
}

// GoMetadata's graph is private. DecodeGoMetadata accepts only diagnostics;
// SupervisedGoMetadata also verifies B's private observation seal. Neither can
// grant omissions, reuse credit, execution admission or check success.
type GoMetadata struct {
	commit, manifest, contextDigest, outputDigest, environment string
	packages                                                   map[string]*goPackage
}

type goPackage struct {
	dir     string
	imports map[string]bool
	files   map[string]bool
}

func normalizedImport(s string) string {
	if i := strings.Index(s, " ["); i >= 0 {
		s = s[:i]
	}
	return s
}

func localImport(s string) bool { return s == goModule || strings.HasPrefix(s, goModule+"/") }

// DecodeGoMetadata binds bounded go-list output to an independently enumerated
// immutable snapshot. A caller-supplied file is diagnostic, never provenance.
// Standard/third-party metadata is retained for error checks, but cannot own
// repository paths. The full source inventory must be represented, including
// ignored build-tag/platform files and external test variants.
func DecodeGoMetadata(s Snapshot, c GoAnalysisContext, raw []byte) (*GoMetadata, error) {
	if len(raw) == 0 || len(raw) > maxGuestOutput || s.ManifestDigest != digest("tree-manifest", s.Entries) || !objectID.MatchString(s.Commit) ||
		c.GOOS != "linux" || c.GOARCH != "amd64" || len(c.Tags) != 0 || !digestID.MatchString(c.ToolchainDigest) || !digestID.MatchString(c.DependencyDigest) || !digestID.MatchString(c.EnvironmentDigest) {
		return nil, fmt.Errorf("unsupported or unbound Go metadata")
	}
	entries := map[string]Entry{}
	for _, e := range s.Entries {
		if e.Mode != "100644" && e.Mode != "100755" {
			return nil, fmt.Errorf("unsupported analysis path mode")
		}
		if e.Path == "go.work" || e.Path == "go.work.sum" || (strings.HasSuffix(e.Path, "/go.mod")) {
			return nil, fmt.Errorf("workspace or nested module unsupported")
		}
		entries[e.Path] = e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	m := &GoMetadata{commit: s.Commit, manifest: s.ManifestDigest, contextDigest: digest("go-analysis-context", c), outputDigest: RawDigest(raw), environment: c.EnvironmentDigest, packages: map[string]*goPackage{}}
	seen := map[string]bool{}
	edges := 0
	for n := 0; ; n++ {
		var value json.RawMessage
		if err := d.Decode(&value); err == io.EOF {
			break
		} else if err != nil || n >= 20000 {
			return nil, fmt.Errorf("invalid or oversized Go metadata stream")
		}
		if err := uniqueJSON(json.NewDecoder(bytes.NewReader(value))); err != nil {
			return nil, err
		}
		var p goListPackage
		// go list has additional version-dependent fields. They are data only;
		// all fields used to narrow the graph are validated below.
		if err := json.Unmarshal(value, &p); err != nil || p.ImportPath == "" || seen[p.ImportPath] || p.Error != nil || p.Incomplete || len(p.DepsErrors) != 0 || (p.Module != nil && p.Module.Replace != nil) {
			return nil, fmt.Errorf("duplicate, incomplete or replaced Go package")
		}
		seen[p.ImportPath] = true
		if len(p.CgoFiles)+len(p.CFiles)+len(p.CXXFiles)+len(p.MFiles)+len(p.HFiles)+len(p.FFiles)+len(p.SFiles)+len(p.SwigFiles)+len(p.SwigCXXFiles)+len(p.SysoFiles) > 0 && localImport(normalizedImport(p.ImportPath)) {
			return nil, fmt.Errorf("unsupported local cgo/assembly inputs")
		}
		pkg := normalizedImport(p.ImportPath)
		if !localImport(pkg) {
			continue
		}
		// Synthetic test main has generated sources in the Go build cache.
		// Its imports are already included by TestImports/XTestImports/variants.
		if strings.HasSuffix(pkg, ".test") && p.Name == "main" {
			continue
		}
		if p.ForTest != "" {
			pkg = normalizedImport(p.ForTest)
		}
		if !localImport(pkg) || p.Module == nil || !p.Module.Main || p.Module.Path != goModule || p.Standard {
			return nil, fmt.Errorf("invalid repository package ownership")
		}
		dir := strings.TrimPrefix(pkg, goModule)
		dir = strings.TrimPrefix(dir, "/")
		wantDir := analysisRoot
		if dir != "" {
			wantDir += "/" + dir
		}
		if p.Dir != wantDir {
			return nil, fmt.Errorf("metadata directory not bound to immutable source")
		}
		g := m.packages[pkg]
		if g == nil {
			g = &goPackage{dir: dir, imports: map[string]bool{}, files: map[string]bool{}}
			m.packages[pkg] = g
		}
		for _, imports := range [][]string{p.Imports, p.TestImports, p.XTestImports} {
			for _, dep := range imports {
				edges++
				if edges > 200000 || dep == "" {
					return nil, fmt.Errorf("Go graph edge limit or invalid import")
				}
				dep = normalizedImport(dep)
				if strings.HasSuffix(dep, "_test") {
					dep = strings.TrimSuffix(dep, "_test")
				}
				if localImport(dep) && dep != pkg {
					g.imports[dep] = true
				}
			}
		}
		for _, names := range [][]string{p.GoFiles, p.TestGoFiles, p.XTestGoFiles, p.IgnoredGoFiles, p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles} {
			for _, name := range names {
				if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") {
					return nil, fmt.Errorf("unsafe metadata file path")
				}
				file := name
				if dir != "" {
					file = dir + "/" + name
				}
				if _, ok := entries[file]; !ok {
					return nil, fmt.Errorf("metadata source missing from immutable tree")
				}
				g.files[file] = true
			}
		}
	}
	if len(m.packages) == 0 {
		return nil, fmt.Errorf("empty repository Go graph")
	}
	for _, g := range m.packages {
		for dep := range g.imports {
			if m.packages[dep] == nil {
				return nil, fmt.Errorf("incomplete local import graph")
			}
		}
	}
	for _, e := range s.Entries {
		if !strings.HasSuffix(e.Path, ".go") || strings.HasPrefix(e.Path, "vendor/") || strings.Contains("/"+e.Path, "/testdata/") {
			continue
		}
		dir := path.Dir(e.Path)
		if dir == "." {
			dir = ""
		}
		pkg := goModule
		if dir != "" {
			pkg += "/" + dir
		}
		g := m.packages[pkg]
		if g == nil || !g.files[e.Path] {
			return nil, fmt.Errorf("Git Go source absent from metadata")
		}
	}
	return m, nil
}

// confirmGoImports independently widens the metadata graph from immutable Go
// blobs. Removing TestImports/XTestImports in a supplied diagnostic artifact
// cannot shrink reverse dependencies. Ignored platform/tag sources also widen
// the graph; unresolved imports (including C) retain full work. No candidate
// code, hooks, helpers or generated source executes during this check.
func confirmGoImports(ctx context.Context, r *Repository, s Snapshot, m *GoMetadata) (*GoMetadata, error) {
	if m == nil {
		return nil, fmt.Errorf("Go metadata unavailable")
	}
	copy := *m
	copy.packages = map[string]*goPackage{}
	for pkg, g := range m.packages {
		next := *g
		next.imports = map[string]bool{}
		for dep := range g.imports {
			next.imports[dep] = true
		}
		copy.packages[pkg] = &next
	}
	raw, err := r.sourceArchive(ctx, s)
	if err != nil {
		return nil, err
	}
	in := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := in.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(h.Name, ".go") || strings.Contains("/"+h.Name, "/testdata/") || strings.HasPrefix(h.Name, "vendor/") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g := copy.packages[localGoOwner(h.Name)]
		if g == nil {
			return nil, fmt.Errorf("Git package absent from analysis")
		}
		f, err := parser.ParseFile(token.NewFileSet(), h.Name, in, parser.ImportsOnly)
		if err != nil {
			return nil, fmt.Errorf("Go source import parse failed")
		}
		for _, im := range f.Imports {
			dep, err := strconv.Unquote(im.Path.Value)
			if err != nil || dep == "C" {
				return nil, fmt.Errorf("unsupported source import")
			}
			if localImport(dep) && dep != localGoOwner(h.Name) {
				if copy.packages[dep] == nil {
					return nil, fmt.Errorf("unresolved source import")
				}
				g.imports[dep] = true
			}
		}
	}
	return &copy, nil
}

// SupervisedGoMetadata consumes only the named metadata artifact from a sealed
// successful B observation with the exact installed metadata recipe. A forged
// artifact or caller-authored go list output cannot acquire supervisor identity.
func SupervisedGoMetadata(s *Supervisor, p Plan, o SupervisedObservation, c GoAnalysisContext) (*GoMetadata, error) {
	if s == nil || s.Verify(p, o) != nil || o.Receipt.Result != "success" || c.EnvironmentDigest != p.EnvironmentDigest || c.ToolchainDigest != s.profile.ToolchainDigest {
		return nil, fmt.Errorf("verified metadata supervisor required")
	}
	want := GoMetadataRecipe(o.Receipt.ObligationID)
	found := false
	for _, recipe := range s.profile.Recipes {
		if digest("metadata-recipe", recipe) == digest("metadata-recipe", want) {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("pinned Go metadata recipe required")
	}
	for _, a := range o.Artifacts {
		if a.Name == want.OutputArtifact {
			return DecodeGoMetadata(p.Candidate, c, a.Data)
		}
	}
	return nil, fmt.Errorf("metadata artifact missing")
}

func goKeys(m map[string]bool) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}
