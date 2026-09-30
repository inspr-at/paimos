// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

func testGuardMaster() []byte {
	return []byte("synthetic-doctrine-guard-key-0123456789")
}

func quoteCorpus(texts ...string) *guardCorpus {
	docs := map[string]string{}
	for i, text := range texts {
		docs[fmt.Sprintf("private/%02d.md", i)] = text
	}
	return corpusFrom(testGuardMaster(), docs)
}

type memReader struct {
	files map[string][]byte
	fail  string
	sizes map[string]int
}

func (m memReader) Commit(context.Context, string, string) (Commit, error) {
	return Commit{}, gitFail("unused")
}

func (m memReader) Tree(context.Context, string, string) ([]Entry, error) {
	out := make([]Entry, 0, len(m.files))
	for path, raw := range m.files {
		size := len(raw)
		if m.sizes != nil {
			if n, ok := m.sizes[path]; ok {
				size = n
			}
		}
		out = append(out, Entry{Path: path, SHA: BlobSHA(raw), Size: size})
	}
	return out, nil
}

func (m memReader) Blob(_ context.Context, _, sha string, _ int) ([]byte, error) {
	for path, raw := range m.files {
		if BlobSHA(raw) != sha {
			continue
		}
		if path == m.fail {
			return nil, gitFail("GitHub answered 502 for the file")
		}
		return append([]byte(nil), raw...), nil
	}
	return nil, gitFail("GitHub has no file here")
}

func TestGuardCorpusRoundTripOmitsPlaintext(t *testing.T) {
	original := quoteCorpus(guardRule, guardTLDR, "commands/secrets stays covered as a whole entry today.")
	raw, err := original.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("copper")) || bytes.Contains(raw, []byte("notebooks")) || bytes.Contains(raw, []byte(guardRule)) {
		t.Fatal("marshaled guard contains plaintext")
	}
	back, err := unmarshalGuard(raw, testGuardMaster())
	if err != nil {
		t.Fatal(err)
	}
	if guardPrivateQuotes(back, nil, guardRule) == nil || guardPrivateQuotes(back, nil, guardTLDR) == nil {
		t.Fatal("round trip dropped a quotation")
	}
	if _, err := unmarshalGuard([]byte("nope"), testGuardMaster()); err == nil {
		t.Fatal("corrupt guard accepted")
	}
}

func TestReadPrivateCorpusFullTreeFailClosed(t *testing.T) {
	profile := "The profile ledger forbids printing fleet hostnames in a public proposal body."
	command := "Never paste an age identity into a chat transcript or a public pull request."
	prose := "Operators keep the silver spare key inside the cedar drawer behind the north stair."
	docs := map[string][]byte{
		"docs/AGENTS-KERNEL-PRIVATE.md":        []byte("# Private\n\n- " + guardRule + "\n\n" + prose + "\n"),
		"docs/AGENTS-PROFILE-MARKUS.md":        []byte("# Profile\n\n- " + profile + "\n"),
		"commands/secrets.md":                  []byte("# Secrets\n\n- " + command + "\n"),
		"docs/AGENTS-KERNEL-PRIVATE.tldr.yaml": []byte("rules:\n  copper:\n    en: " + guardTLDR + "\n"),
		"assets/blank.dat":                     {0, 1, 2, 3},
		"notes/blank.md":                       []byte("   \n"),
	}
	ctx := context.Background()
	raw, err := readPrivateCorpus(ctx, memReader{files: docs, fail: "docs/AGENTS-PROFILE-MARKUS.md"}, privateRepository, fixtureCommit, testGuardMaster())
	if err == nil || raw != nil {
		t.Fatal("unreadable blob did not fail closed")
	}
	raw, err = readPrivateCorpus(ctx, memReader{files: docs}, privateRepository, fixtureCommit, testGuardMaster())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(profile)) || bytes.Contains(raw, []byte(command)) || bytes.Contains(raw, []byte("profile")) {
		t.Fatal("corpus stored plaintext")
	}
	corpus, err := unmarshalGuard(raw, testGuardMaster())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{guardRule, guardTLDR, profile, command, prose} {
		if guardPrivateQuotes(corpus, nil, secret) == nil {
			t.Fatalf("full tree missed a private span")
		}
	}
	// Binary and whitespace do not erase the rest of the tree.
	if guardPrivateQuotes(corpus, nil, "Run tests before merging.") != nil {
		t.Fatal("unrelated public sentence refused")
	}
	bad := docs
	bad["notes/odd.md"] = []byte{0xff, 0xfe, 'a'}
	if _, err := readPrivateCorpus(ctx, memReader{files: bad}, privateRepository, fixtureCommit, testGuardMaster()); err == nil {
		t.Fatal("invalid UTF-8 did not fail closed")
	}
	if _, err := readPrivateCorpus(ctx, memReader{files: map[string][]byte{}, sizes: nil}, privateRepository, fixtureCommit, testGuardMaster()); err == nil {
		t.Fatal("empty tree did not fail closed")
	}
	over := map[string][]byte{"docs/AGENTS-KERNEL-PRIVATE.md": []byte(guardRule)}
	if _, err := readPrivateCorpus(ctx, memReader{files: over, sizes: map[string]int{"docs/AGENTS-KERNEL-PRIVATE.md": maxGuardFile + 1}}, privateRepository, fixtureCommit, testGuardMaster()); err == nil {
		t.Fatal("oversized file did not fail closed")
	}
	wide := map[string][]byte{
		"docs/OK.md":   []byte(guardRule),
		"docs/WIDE.md": utf16LE("The private line must not be skipped as binary text."),
	}
	if _, err := readPrivateCorpus(ctx, memReader{files: wide}, privateRepository, fixtureCommit, testGuardMaster()); err == nil {
		t.Fatal("UTF-16 text was skipped as binary")
	}
	broken := map[string][]byte{"docs/OK.md": []byte(guardRule), "notes/broken.md": {'h', 0, 'i'}}
	if _, err := readPrivateCorpus(ctx, memReader{files: broken}, privateRepository, fixtureCommit, testGuardMaster()); err == nil {
		t.Fatal("undecodable text file was skipped")
	}
	if _, err := readPrivateCorpus(ctx, memReader{files: wide}, privateRepository, fixtureCommit, nil); err == nil || strings.Contains(err.Error(), "synthetic-doctrine") {
		t.Fatal("missing guard key was accepted or reflected")
	}
	if utf16Text([]byte{0, 1, 2, 3}) {
		t.Fatal("binary fixture classified as UTF-16")
	}
}

func utf16LE(s string) []byte {
	out := make([]byte, len(s)*2)
	for i := 0; i < len(s); i++ {
		out[i*2] = s[i]
	}
	return out
}

func TestGuardHMACIsNotReversibleFromTheCorpus(t *testing.T) {
	key := testGuardMaster()
	raw, err := corpusFrom(key, map[string]string{"docs/private.md": guardRule}).marshal()
	if err != nil {
		t.Fatal(err)
	}
	words := proposalWords(guardRule)
	if len(words) < quoteRunWords {
		t.Fatal("fixture quotation is shorter than a run")
	}
	sum := sha256.Sum256([]byte(strings.Join(words[:quoteRunWords], "\x00")))
	if bytes.Contains(raw, key) || bytes.Contains(raw, sum[:]) || bytes.Contains(raw, []byte("copper")) {
		t.Fatal("corpus exposes the key, an unkeyed hash, or plaintext")
	}
	other := bytes.Repeat([]byte{9}, 32)
	alt, err := corpusFrom(other, map[string]string{"docs/private.md": guardRule}).marshal()
	if err != nil || bytes.Equal(raw, alt) {
		t.Fatal("a different key produced the same corpus")
	}
	if _, err := unmarshalGuard(raw, other); err == nil {
		t.Fatal("corpus opened with the wrong key")
	}
	v1 := append([]byte{'P', 'G', 1}, raw[3:]...)
	if _, err := unmarshalGuard(v1, key); err == nil {
		t.Fatal("version 1 corpus accepted")
	}
	a := deriveGuardKey(key, "11111111-1111-4111-8111-111111111111")
	b := deriveGuardKey(key, "22222222-2222-4222-8222-222222222222")
	if len(a) != 32 || bytes.Equal(a, b) || bytes.Equal(a, key) || deriveGuardKey(key[:31], "tenant") != nil || deriveGuardKey(key, "") != nil {
		t.Fatal("tenant key derivation is not separated")
	}
}

func TestMainMatchingFilesIgnoresOtherPins(t *testing.T) {
	mainFiles := fixtureFiles()
	cached := make([]File, 0, len(mainFiles))
	tree := make([]Entry, 0, len(mainFiles))
	for path, content := range mainFiles {
		raw := []byte(content)
		cached = append(cached, File{Path: path, BlobSHA: BlobSHA(raw), Content: append([]byte(nil), raw...)})
		tree = append(tree, Entry{Path: path, SHA: BlobSHA(raw)})
	}
	for i := range cached {
		if cached[i].Path != "docs/AGENTS-DOMAIN-DEV.md" {
			continue
		}
		cached[i].Content = append(cached[i].Content, []byte("\n- "+guardRule+"\n")...)
		cached[i].BlobSHA = BlobSHA(cached[i].Content)
	}
	matched := mainMatchingFiles(cached, tree)
	if len(matched) != len(cached)-1 {
		t.Fatalf("matched %d files", len(matched))
	}
	for _, file := range matched {
		if bytes.Contains(file.Content, []byte("copper observatory")) {
			t.Fatal("pin-only blob stayed in the exemption")
		}
	}
	corpus := quoteCorpus(guardRule)
	if guardPrivateQuotes(corpus, matched, guardRule) == nil {
		t.Fatal("pin-only private rule was exempted")
	}
	if guardPrivateQuotes(corpus, cached, guardRule) != nil {
		t.Fatal("the same pin bytes would have been treated as public")
	}
	extra := File{Path: "docs/UNMERGED.md", BlobSHA: BlobSHA([]byte(guardRule)), Content: []byte(guardRule)}
	if n := len(mainMatchingFiles(append(append([]File{}, matched...), extra), tree)); n != len(matched) {
		t.Fatalf("unmerged blob exempted, matched %d", n)
	}
}

func TestGuardAttackClassesAndPublicOverlap(t *testing.T) {
	profile := "The profile ledger forbids printing fleet hostnames in a public proposal body."
	command := "Never paste an age identity into a chat transcript or a public pull request."
	prose := "Operators keep the silver spare key inside the cedar drawer behind the north stair."
	paragraph := "The copper observatory keeps seven violet notebooks beneath the eastern stairway. Seasonal planning uses the silver ledger beside the northern window each week."
	reordered := "Seasonal planning uses the silver ledger beside the northern window each week. The copper observatory keeps seven violet notebooks beneath the eastern stairway."
	shared := "Run tests before merging the documented public change today."
	docs := map[string]string{
		"docs/AGENTS-KERNEL-PRIVATE.md": "# Private\n\n" + shared + "\n\n" + prose + "\n\n" + paragraph + "\n",
		"docs/AGENTS-PROFILE-MARKUS.md": "- " + profile + "\n",
		"commands/secrets.md":           "- " + command + "\n",
		"rules.tldr.yaml":               "rules:\n  copper:\n    en: " + guardTLDR + "\n",
	}
	corpus := corpusFrom(testGuardMaster(), docs)
	public := []File{{Path: "docs/AGENTS-KERNEL.md", Content: []byte("# Public\n\n- " + shared + "\n")}}
	if err := guardPrivateQuotes(corpus, public, shared); err != nil {
		t.Fatalf("shared public span refused: %v", err)
	}
	attacks := []struct {
		name string
		text string
	}{
		{"verbatim", guardRule},
		{"zero-width", strings.ReplaceAll(paragraph, "o", "o\u200b")},
		{"fullwidth", fullwidth(prose)},
		{"cyrillic", cyrillicize(command)},
		{"split-lines", strings.ReplaceAll(profile, " ", "\n")},
		{"reordered", reordered},
		{"profile", profile},
		{"prose", prose},
		{"commands", command},
		{"tldr", guardTLDR},
	}
	// guardRule's opening six words sit inside paragraph, so verbatim still hits.
	for _, attack := range attacks {
		if err := guardPrivateQuotes(corpus, public, attack.text); err == nil {
			t.Errorf("attack %s accepted", attack.name)
		} else if strings.Contains(err.Error(), "copper") || strings.Contains(err.Error(), "profile") || strings.Contains(err.Error(), "age identity") {
			t.Errorf("attack %s reflected", attack.name)
		}
	}
	five := "Copper notebooks stay hidden underground."
	if guardPrivateQuotes(quoteCorpus(five), nil, five) == nil {
		t.Fatal("five-word whole entry accepted")
	}
	six := "Copper notebooks stay hidden underground today."
	if guardPrivateQuotes(quoteCorpus("prefix "+six), nil, "Keep "+six+" safe.") == nil {
		t.Fatal("six-word run accepted")
	}
}

func TestUnchangedFixtureRulesAreNotPrivateQuotes(t *testing.T) {
	docs := fixtureFiles()
	docs["docs/AGENTS-KERNEL-PRIVATE.md"] = fixtureKernel + "\n\n" + guardRule + "\n"
	docs["commands/secrets.md"] = "- Never paste an age identity into a chat transcript or a public pull request.\n"
	corpus := corpusFrom(testGuardMaster(), docs)
	var public []File
	for path, content := range fixtureFiles() {
		public = append(public, File{Path: path, Content: []byte(content)})
	}
	views := Render(publicRepository, fixtureCommit, false, public)
	checked := 0
	for _, view := range views {
		for _, rule := range view.Rules {
			checked++
			texts := []string{rule.Source, rule.Text}
			if rule.TLDR != nil {
				texts = append(texts, rule.TLDR.EN, rule.TLDR.DE)
			}
			if err := guardPrivateQuotes(corpus, public, texts...); err != nil {
				t.Fatalf("public rule refused %s:%d", view.Path, rule.StartLine)
			}
		}
	}
	if checked == 0 {
		t.Fatal("fixture produced no public rules")
	}
	if guardPrivateQuotes(corpus, public, guardRule) == nil {
		t.Fatal("private sentence inside a copied public file was allowed")
	}
}

func TestCheckedOutDoctrineGuardCounts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(home, "Code", "inspr-modules")
	private := filepath.Join(home, "Code", "inspr-doctrine-private")
	if _, err := os.Stat(modules); err != nil {
		t.Skip("public doctrine checkout absent")
	}
	if _, err := os.Stat(private); err != nil {
		t.Skip("private doctrine checkout absent")
	}
	pubDocs := gitTexts(t, modules)
	privDocs := gitTexts(t, private)
	public := indexedDoctrineFiles(pubDocs)
	views := Render(publicRepository, fixtureCommit, false, public)
	corpus := corpusFrom(testGuardMaster(), privDocs)
	raw, err := corpus.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("doctrine")) || bytes.Contains(raw, []byte("AGENTS")) {
		t.Fatal("measured corpus looks like plaintext")
	}
	checked, refused := 0, 0
	allow := allowFromFiles(testGuardMaster(), public)
	for _, view := range views {
		for _, rule := range view.Rules {
			checked++
			texts := []string{rule.Source, rule.Text}
			if rule.TLDR != nil {
				texts = append(texts, rule.TLDR.EN, rule.TLDR.DE)
			}
			if guardPrivateQuotes(corpus, public, texts...) == nil {
				continue
			}
			refused++
			kind := "other"
			for _, text := range texts {
				words := proposalWords(text)
				switch {
				case corpus.runHit(allow, words):
					kind = "run"
				case corpus.wholeHit(allow, words):
					kind = "whole"
				case corpus.shingleHit(allow, words):
					kind = "shingle"
				}
			}
			t.Errorf("public rule refused %s:%d kind=%s", view.Path, rule.StartLine, kind)
		}
	}
	if checked == 0 {
		t.Fatal("public checkout produced no indexed rules")
	}
	lines, wholes, runs, refusedLines, privateOnlyAllowed := 0, 0, 0, 0, 0
	profileFiles, commandFiles := 0, 0
	for path, text := range privDocs {
		if strings.Contains(path, "AGENTS-PROFILE") {
			profileFiles++
		}
		if strings.HasPrefix(path, "commands/") {
			commandFiles++
		}
		for n, line := range strings.Split(text, "\n") {
			words := proposalWords(line)
			if len(words) >= quoteWholeMin {
				wholes++
			}
			if len(words) < quoteRunWords {
				continue
			}
			lines++
			runs += len(words) - quoteRunWords + 1
			if corpus.quotes(allow, line) {
				refusedLines++
				continue
			}
			if corpus.runHit(allow, words) {
				privateOnlyAllowed++
				t.Errorf("private line allowed with a private-only run %s:%d", path, n+1)
			}
		}
	}
	narrow := map[string]string{}
	for path, text := range privDocs {
		if strings.Contains(path, "AGENTS-PROFILE") || strings.HasPrefix(path, "commands/") {
			continue
		}
		narrow[path] = text
	}
	narrowRaw, err := corpusFrom(testGuardMaster(), narrow).marshal()
	if err != nil {
		t.Fatal(err)
	}
	if (profileFiles > 0 || commandFiles > 0) && bytes.Equal(raw, narrowRaw) {
		t.Fatal("profile or commands files did not change the guard")
	}
	t.Logf("public_rules=%d refused=%d private_files=%d profile_files=%d command_files=%d private_lines_ge6=%d private_lines_ge5=%d private_runs=%d refused_lines=%d private_only_allowed=%d corpus_bytes=%d",
		checked, refused, len(privDocs), profileFiles, commandFiles, lines, wholes, runs, refusedLines, privateOnlyAllowed, len(raw))
	if refused != 0 || privateOnlyAllowed != 0 {
		t.Fatalf("guard counts public_refused=%d private_only_allowed=%d", refused, privateOnlyAllowed)
	}
}

func indexedDoctrineFiles(docs map[string]string) []File {
	var files []File
	var wanted []string
	for path, content := range docs {
		if !strings.HasSuffix(path, ".md") || !selects(DefaultPaths, path) {
			continue
		}
		if rulesimport.Prohibited(path) != nil || !rulesimport.Classifies(path) {
			continue
		}
		wanted = append(wanted, path)
		files = append(files, File{Path: path, Content: []byte(content)})
	}
	for _, path := range wanted {
		side := SidecarPath(path)
		if content, ok := docs[side]; ok {
			files = append(files, File{Path: side, Content: []byte(content)})
		}
	}
	return files
}

func gitTexts(t *testing.T, dir string) map[string]string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files %s: %v", dir, err)
	}
	docs := map[string]string{}
	textFail := 0
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		path := string(name)
		full := filepath.Join(dir, path)
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("unreadable %s", path)
		}
		// Git trees index regular files. Symlinks are not doctrine text.
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxGuardFile {
			t.Fatalf("oversized %s", path)
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("unreadable %s", path)
		}
		if utf16Text(raw) || (doctrineTextPath(path) && (bytes.Contains(raw, []byte{0}) || !utf8Valid(raw))) || (!bytes.Contains(raw, []byte{0}) && !utf8Valid(raw)) {
			textFail++
			continue
		}
		if bytes.Contains(raw, []byte{0}) {
			continue
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		docs[path] = string(raw)
	}
	if textFail > 0 {
		t.Fatalf("undecodable text files=%d", textFail)
	}
	if len(docs) == 0 {
		t.Fatalf("no text files in %s", dir)
	}
	return docs
}

func utf8Valid(raw []byte) bool {
	return strings.ToValidUTF8(string(raw), "") == string(raw)
}
