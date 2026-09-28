// SPDX-License-Identifier: AGPL-3.0-only

// Package quoteshowcase applies a directory of fictional showcase quotes through
// the quote and CRM services. Adding another quotes or organisations file does
// not require a code change.
package quoteshowcase

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/business/crm"
	"github.com/inspr-at/paimos/internal/business/quotes"
)

const schemaName = "aeon.quote-showcase.v1"
const maxBundle = 64 << 20

var showcaseKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Classic quote exports store layout measurements as JSON numbers. The quote
// document keeps them as exact one-decimal strings. Strings already in that
// form are left unchanged.
var millimetreFields = map[string]bool{
	"marker_x_mm": true, "marker_y_mm": true, "text_start_mm": true,
	"spacing_before_mm": true, "spacing_after_mm": true,
}
var exactMillimetres = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9])?$`)

// Bundle is a showcase directory: one manifest, organisation files, quote files and optional profiles.
type Bundle struct {
	Features      map[string]string
	AddFiles      string
	Organisations []Organisation
	Quotes        []QuoteSpec
	Profiles      []ProfileSpec
}

// Organisation is one customer, including its contacts.
type Organisation struct {
	Key         string
	Name        string
	LegalName   string
	Industry    string
	Website     string
	Phone       string
	VATID       string
	RegisterNo  string
	Description string
	Currency    string
	Billing     crm.Address
	Contacts    []Contact
}

// Contact is a person at the organisation. Principal is set when a quote is accepted in their name.
type Contact struct {
	Key       string
	Name      string
	Email     string
	Phone     string
	Role      string
	Primary   bool
	Principal bool
}

// QuoteSpec is one showcase quote. Dates are computed when the bundle is applied.
type QuoteSpec struct {
	Key          string
	Organisation string
	Contact      string
	ProfileName  string
	State        string
	PublicLink   bool
	ValidityDays int
	Versions     []quotes.ShowcaseDraft
}

// ProfileSpec is a quote-profile creation payload applied before the quotes.
type ProfileSpec struct {
	Name string
	Raw  json.RawMessage
}

type manifestFile struct {
	Schema   string            `json:"schema"`
	Features map[string]string `json:"features"`
	AddFiles string            `json:"add_files"`
}

type organisationFile struct {
	Schema      string        `json:"schema"`
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	LegalName   string        `json:"legal_name"`
	Industry    string        `json:"industry"`
	Website     string        `json:"website"`
	Phone       string        `json:"phone"`
	VATID       string        `json:"vat_id"`
	RegisterNo  string        `json:"register_no"`
	Description string        `json:"description"`
	Currency    string        `json:"currency"`
	Billing     crm.Address   `json:"billing_address"`
	Contacts    []contactFile `json:"contacts"`
}

type contactFile struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	Role      string `json:"role"`
	Primary   bool   `json:"primary"`
	Principal bool   `json:"principal,omitempty"`
}

type quoteFile struct {
	Schema       string                 `json:"schema"`
	Key          string                 `json:"key"`
	Organisation string                 `json:"organisation"`
	Contact      string                 `json:"contact"`
	ProfileName  string                 `json:"profile_name,omitempty"`
	State        string                 `json:"state"`
	PublicLink   bool                   `json:"public_link,omitempty"`
	ValidityDays int                    `json:"validity_days"`
	Versions     []quotes.ShowcaseDraft `json:"versions"`
}

// ReadDir loads a showcase bundle. Symlinks and paths outside the directory are rejected.
func ReadDir(root string) (Bundle, error) {
	files := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		name = filepath.ToSlash(name)
		if !safePath(name) || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid bundle path %q", name)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("nonregular bundle file %q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 10<<20 || total+info.Size() > maxBundle {
			return errors.New("showcase bundle exceeds size limit")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		total += int64(len(data))
		files[name] = data
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	return parseBundle(files)
}

// ReadTar reads a showcase bundle from tar. Absolute paths, traversal, links and duplicates are rejected.
func ReadTar(src io.Reader) (Bundle, error) {
	files := map[string][]byte{}
	reader := tar.NewReader(io.LimitReader(src, maxBundle+1))
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Bundle{}, err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !safePath(name) {
			return Bundle{}, fmt.Errorf("invalid bundle path %q", header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return Bundle{}, fmt.Errorf("nonregular bundle file %q", name)
		}
		if _, exists := files[name]; exists || header.Size < 0 || header.Size > 10<<20 || total+header.Size > maxBundle {
			return Bundle{}, fmt.Errorf("duplicate or oversized bundle file %q", name)
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return Bundle{}, fmt.Errorf("invalid bundle file %q", name)
		}
		total += int64(len(data))
		files[name] = data
	}
	return parseBundle(files)
}

func safePath(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

func parseBundle(files map[string][]byte) (Bundle, error) {
	var bundle Bundle
	raw, ok := files["manifest.json"]
	if !ok {
		return bundle, errors.New("bundle requires manifest.json")
	}
	var manifest manifestFile
	if err := decode(raw, &manifest); err != nil {
		return bundle, fmt.Errorf("manifest.json: %w", err)
	}
	if manifest.Schema != schemaName || len(manifest.Features) == 0 || strings.TrimSpace(manifest.AddFiles) == "" {
		return bundle, errors.New("manifest.json requires schema, features and add_files")
	}
	bundle.Features = manifest.Features
	bundle.AddFiles = manifest.AddFiles
	var orgNames, quoteNames, profileNames []string
	for name := range files {
		switch {
		case name == "manifest.json":
		case strings.HasPrefix(name, "organisations/") && strings.HasSuffix(name, ".json") && strings.Count(name, "/") == 1:
			orgNames = append(orgNames, name)
		case strings.HasPrefix(name, "quotes/") && strings.HasSuffix(name, ".json") && strings.Count(name, "/") == 1:
			quoteNames = append(quoteNames, name)
		case strings.HasPrefix(name, "profiles/") && strings.HasSuffix(name, ".json") && strings.Count(name, "/") == 1:
			profileNames = append(profileNames, name)
		default:
			return bundle, fmt.Errorf("unexpected bundle file %q", name)
		}
	}
	sort.Strings(orgNames)
	sort.Strings(quoteNames)
	sort.Strings(profileNames)
	if len(orgNames) == 0 || len(quoteNames) == 0 {
		return bundle, errors.New("bundle requires organisations and quotes")
	}
	orgs := map[string]Organisation{}
	contacts := map[string]Contact{}
	contactOrg := map[string]string{}
	for _, name := range orgNames {
		var file organisationFile
		if err := decode(files[name], &file); err != nil {
			return bundle, fmt.Errorf("%s: %w", name, err)
		}
		org, err := organisationFrom(file)
		if err != nil {
			return bundle, fmt.Errorf("%s: %w", name, err)
		}
		if _, exists := orgs[org.Key]; exists {
			return bundle, fmt.Errorf("duplicate organisation key %q", org.Key)
		}
		for _, contact := range org.Contacts {
			if _, exists := contacts[contact.Key]; exists {
				return bundle, fmt.Errorf("duplicate contact key %q", contact.Key)
			}
			contacts[contact.Key] = contact
			contactOrg[contact.Key] = org.Key
		}
		orgs[org.Key] = org
		bundle.Organisations = append(bundle.Organisations, org)
	}
	profiles := map[string]bool{}
	for _, name := range profileNames {
		var header struct {
			Name       string          `json:"name"`
			Definition json.RawMessage `json:"definition"`
		}
		if err := decode(files[name], &header); err != nil {
			return bundle, fmt.Errorf("%s: %w", name, err)
		}
		header.Name = strings.TrimSpace(header.Name)
		if header.Name == "" || len(header.Name) > 100 || len(header.Definition) == 0 {
			return bundle, fmt.Errorf("%s: invalid profile", name)
		}
		if profiles[header.Name] {
			return bundle, fmt.Errorf("duplicate profile %q", header.Name)
		}
		profiles[header.Name] = true
		bundle.Profiles = append(bundle.Profiles, ProfileSpec{Name: header.Name, Raw: append([]byte(nil), files[name]...)})
	}
	seen := map[string]bool{}
	for _, name := range quoteNames {
		normalized, err := coerceMillimetreNumbers(files[name])
		if err != nil {
			return bundle, fmt.Errorf("%s: %w", name, err)
		}
		var file quoteFile
		if err := decode(normalized, &file); err != nil {
			return bundle, fmt.Errorf("%s: %w", name, err)
		}
		quote, err := quoteFrom(file, orgs, contacts, contactOrg)
		if err != nil {
			return bundle, fmt.Errorf("%s: %w", name, err)
		}
		if seen[quote.Key] {
			return bundle, fmt.Errorf("duplicate quote key %q", quote.Key)
		}
		seen[quote.Key] = true
		bundle.Quotes = append(bundle.Quotes, quote)
	}
	return bundle, nil
}

func coerceMillimetreNumbers(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON value")
	}
	if err := walkMillimetres(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func walkMillimetres(value any) error {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			number, numeric := child.(json.Number)
			if millimetreFields[key] && numeric {
				text := number.String()
				if !exactMillimetres.MatchString(text) {
					return fmt.Errorf("%s has unsupported precision", key)
				}
				node[key] = text
				continue
			}
			if err := walkMillimetres(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := walkMillimetres(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func decode(raw []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("must contain one JSON value")
	}
	return nil
}

func organisationFrom(file organisationFile) (Organisation, error) {
	var org Organisation
	if file.Schema != schemaName || !showcaseKey.MatchString(file.Key) {
		return org, errors.New("invalid organisation schema or key")
	}
	if strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.LegalName) == "" || file.Currency != "EUR" {
		return org, errors.New("organisation needs a name, legal name and EUR currency")
	}
	if len(file.Contacts) == 0 {
		return org, errors.New("organisation needs a contact")
	}
	org = Organisation{Key: file.Key, Name: strings.TrimSpace(file.Name), LegalName: strings.TrimSpace(file.LegalName), Industry: file.Industry, Website: file.Website, Phone: file.Phone, VATID: file.VATID, RegisterNo: file.RegisterNo, Description: file.Description, Currency: file.Currency, Billing: file.Billing}
	primary := 0
	seen := map[string]bool{}
	for _, contact := range file.Contacts {
		if !showcaseKey.MatchString(contact.Key) || strings.TrimSpace(contact.Name) == "" || strings.TrimSpace(contact.Email) == "" {
			return org, fmt.Errorf("invalid contact %q", contact.Key)
		}
		if seen[contact.Key] {
			return org, fmt.Errorf("duplicate contact key %q", contact.Key)
		}
		seen[contact.Key] = true
		if contact.Primary {
			primary++
		}
		org.Contacts = append(org.Contacts, Contact{Key: contact.Key, Name: strings.TrimSpace(contact.Name), Email: contact.Email, Phone: contact.Phone, Role: contact.Role, Primary: contact.Primary, Principal: contact.Principal})
	}
	if primary != 1 {
		return org, errors.New("organisation needs exactly one primary contact")
	}
	sort.SliceStable(org.Contacts, func(i, j int) bool {
		if org.Contacts[i].Primary != org.Contacts[j].Primary {
			return org.Contacts[i].Primary
		}
		return org.Contacts[i].Key < org.Contacts[j].Key
	})
	return org, nil
}

func quoteFrom(file quoteFile, orgs map[string]Organisation, contacts map[string]Contact, contactOrg map[string]string) (QuoteSpec, error) {
	var quote QuoteSpec
	if file.Schema != schemaName || !showcaseKey.MatchString(file.Key) {
		return quote, errors.New("invalid quote schema or key")
	}
	if _, ok := orgs[file.Organisation]; !ok {
		return quote, errors.New("quote organisation or contact is missing")
	}
	contact, ok := contacts[file.Contact]
	if !ok || contactOrg[file.Contact] != file.Organisation {
		return quote, errors.New("quote organisation or contact is missing")
	}
	if file.State != "draft" && file.State != "issued" && file.State != "accepted" {
		return quote, errors.New("quote state must be draft, issued or accepted")
	}
	if file.ValidityDays < 1 || file.ValidityDays > 366 {
		return quote, errors.New("validity_days must be from 1 to 366")
	}
	if len(file.Versions) == 0 || len(file.Versions) > 2 {
		return quote, errors.New("quote needs one or two versions")
	}
	if len(file.Versions) == 2 && file.State != "issued" {
		return quote, errors.New("two versions require state issued")
	}
	if file.State != "issued" && len(file.Versions) != 1 {
		return quote, errors.New("draft and accepted quotes have one version")
	}
	if file.PublicLink && (file.State != "issued" || len(file.Versions) != 1) {
		return quote, errors.New("public_link requires one issued version")
	}
	if file.State == "accepted" && !contact.Principal {
		return quote, errors.New("accepted quote contact needs a principal")
	}
	for i, version := range file.Versions {
		if strings.TrimSpace(version.Title) == "" || version.Currency != "EUR" || len(version.Positions) == 0 {
			return quote, fmt.Errorf("version %d needs a title, EUR currency and a position", i+1)
		}
	}
	return QuoteSpec{Key: file.Key, Organisation: file.Organisation, Contact: file.Contact, ProfileName: file.ProfileName, State: file.State, PublicLink: file.PublicLink, ValidityDays: file.ValidityDays, Versions: file.Versions}, nil
}
