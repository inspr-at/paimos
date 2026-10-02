// SPDX-License-Identifier: AGPL-3.0-only

package quoteshowcase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/business/crm"
	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Report is the dry-run or apply result. It never includes a public-link token.
type Report struct {
	Applied       bool              `json:"applied"`
	Archives      []ArchiveItem     `json:"archives"`
	Organisations []RecordItem      `json:"organisations"`
	Contacts      []ContactItem     `json:"contacts"`
	Profiles      []ProfileItem     `json:"profiles"`
	Quotes        []QuoteItem       `json:"quotes"`
	Features      map[string]string `json:"features"`
	AddFiles      string            `json:"add_files"`
}

// ArchiveItem is one explicit quote the command archives.
type ArchiveItem struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	OfferNo string `json:"offer_no,omitempty"`
	Title   string `json:"title,omitempty"`
}

// RecordItem is an organisation or profile-sized create result.
type RecordItem struct {
	Key    string `json:"key"`
	Action string `json:"action"`
	Name   string `json:"name"`
}

// ContactItem is one contact. Principal is none, create or unchanged.
type ContactItem struct {
	Key       string `json:"key"`
	Action    string `json:"action"`
	Name      string `json:"name"`
	Principal string `json:"principal"`
}

// ProfileItem is one document profile from the bundle.
type ProfileItem struct {
	Name      string `json:"name"`
	Action    string `json:"action"`
	Applied   bool   `json:"applied"`
	ProfileID string `json:"profile_id,omitempty"`
}

// QuoteItem is one showcase quote. PublicLink is none, create, unchanged or needs_link_key.
type QuoteItem struct {
	Key        string `json:"key"`
	Action     string `json:"action"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Versions   int    `json:"versions"`
	PublicLink string `json:"public_link"`
	OfferNo    string `json:"offer_no,omitempty"`
	ID         string `json:"id,omitempty"`
}

type storedContact struct {
	ID          string
	PrincipalID string
	Name        string
	Email       string
}

// Apply plans or writes the bundle. Nothing is written unless apply is true.
// Re-running keeps existing showcase keys unchanged and does not create them again.
func Apply(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, filesDir string, bundle Bundle, archiveIDs []string, linkKey []byte, apply bool) (Report, error) {
	report := Report{Applied: apply, Archives: []ArchiveItem{}, Organisations: []RecordItem{}, Contacts: []ContactItem{}, Profiles: []ProfileItem{}, Quotes: []QuoteItem{}, Features: bundle.Features, AddFiles: bundle.AddFiles}
	if pool == nil || !uuidRe.MatchString(tenantID) || (actorID != "" && !uuidRe.MatchString(actorID)) {
		return report, errors.New("invalid tenant or actor")
	}
	if len(linkKey) == 0 {
		linkKey = nil
	}
	seen := map[string]bool{}
	for _, id := range archiveIDs {
		if !uuidRe.MatchString(id) {
			return report, fmt.Errorf("archive quote %s: invalid id", id)
		}
		if seen[strings.ToLower(id)] {
			return report, fmt.Errorf("archive quote %s: duplicate id", id)
		}
		seen[strings.ToLower(id)] = true
	}
	// Profiles, archives, organisations and quotes commit together. A missing
	// archive id or link key rolls profile changes back with the rest.
	err := db.InTransaction(ctx, pool, func(txCtx context.Context) error {
		if apply && len(bundle.Profiles) > 0 {
			profiles := make([]quotes.ProfileBundle, 0, len(bundle.Profiles))
			for _, profile := range bundle.Profiles {
				profiles = append(profiles, quotes.ProfileBundle{Profile: profile.Raw, Files: profile.Files})
			}
			if err := quotes.LockProfileBundles(txCtx, pool, tenantID, profiles...); err != nil {
				return err
			}
		}
		for _, profile := range bundle.Profiles {
			result, err := quotes.ApplyProfileBundle(txCtx, pool, tenantID, actorID, filesDir, "", quotes.ProfileBundle{Profile: profile.Raw, Files: profile.Files}, false, apply)
			if err != nil {
				return fmt.Errorf("profile %s: %w", profile.Name, err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return err
			}
			var item ProfileItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			item.Name = profile.Name
			report.Profiles = append(report.Profiles, item)
		}
		return db.InTenant(db.AllProjects(txCtx, "quote showcase bundle"), pool, tenantID, func(tx pgx.Tx) error {
			if apply {
				var locked string
				if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, tenantID).Scan(&locked); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":quote-showcase"); err != nil {
					return err
				}
			}
			actor, err := resolveActor(ctx, tx, tenantID, actorID, apply)
			if err != nil {
				return err
			}
			settings, err := quotes.LoadShowcaseSettings(ctx, tx)
			if err != nil {
				return err
			}
			if settings.Revision == 0 {
				return errors.New("quote settings must be configured")
			}
			if err := senderReady(settings.Sender); err != nil {
				return err
			}
			day, err := quotes.ShowcaseDay(settings, time.Now())
			if err != nil {
				return err
			}
			for _, id := range archiveIDs {
				if !apply {
					row, err := quotes.ReadShowcaseQuote(ctx, tx, id)
					if err != nil {
						return fmt.Errorf("archive quote %s: quote not found", id)
					}
					action := "archive"
					if row.Archived {
						action = "already_archived"
					}
					report.Archives = append(report.Archives, ArchiveItem{ID: id, Action: action, OfferNo: row.OfferNo, Title: row.Title})
					continue
				}
				row, changed, err := quotes.ArchiveShowcaseQuote(ctx, tx, actor, id)
				if err != nil {
					return err
				}
				action := "already_archived"
				if changed {
					action = "archive"
				}
				report.Archives = append(report.Archives, ArchiveItem{ID: row.ID, Action: action, OfferNo: row.OfferNo, Title: row.Title})
			}
			orgIDs := map[string]string{}
			contactIDs := map[string]storedContact{}
			for _, org := range bundle.Organisations {
				existing, err := quotes.FindShowcaseKey(ctx, tx, "organisation", org.Key)
				if err != nil {
					return err
				}
				if existing != "" {
					orgIDs[org.Key] = existing
					report.Organisations = append(report.Organisations, RecordItem{Key: org.Key, Action: "unchanged", Name: org.Name})
				} else if !apply {
					report.Organisations = append(report.Organisations, RecordItem{Key: org.Key, Action: "create", Name: org.Name})
				} else {
					created, err := crm.InsertCustomer(ctx, tx, actor, crm.CustomerWrite{Name: org.Name, CustomerFields: crm.CustomerFields{LegalName: org.LegalName, Industry: org.Industry, Website: org.Website, Phone: org.Phone, Description: org.Description, VATID: org.VATID, RegisterNo: org.RegisterNo, Currency: org.Currency, BillingAddress: &org.Billing}}, map[string]string{"showcase_key": org.Key})
					if err != nil {
						return fmt.Errorf("organisation %s: %w", org.Key, err)
					}
					orgIDs[org.Key] = created.ID
					report.Organisations = append(report.Organisations, RecordItem{Key: org.Key, Action: "create", Name: org.Name})
				}
				for _, contact := range org.Contacts {
					existingID, err := quotes.FindShowcaseKey(ctx, tx, "contact", contact.Key)
					if err != nil {
						return err
					}
					if existingID != "" {
						var principalID string
						if err := tx.QueryRow(ctx, `SELECT coalesce(fields->>'showcase_principal_id','') FROM nodes WHERE id=$1::uuid`, existingID).Scan(&principalID); err != nil {
							return err
						}
						principal := "none"
						if contact.Principal {
							if principalID == "" {
								return fmt.Errorf("contact %s exists without its acceptance principal", contact.Key)
							}
							principal = "unchanged"
						}
						contactIDs[contact.Key] = storedContact{ID: existingID, PrincipalID: principalID, Name: contact.Name, Email: contact.Email}
						report.Contacts = append(report.Contacts, ContactItem{Key: contact.Key, Action: "unchanged", Name: contact.Name, Principal: principal})
						continue
					}
					principal := "none"
					if contact.Principal {
						principal = "create"
					}
					if !apply {
						report.Contacts = append(report.Contacts, ContactItem{Key: contact.Key, Action: "create", Name: contact.Name, Principal: principal})
						continue
					}
					if orgIDs[org.Key] == "" {
						return fmt.Errorf("organisation %s was not created", org.Key)
					}
					var principalID string
					if contact.Principal {
						var err error
						principalID, err = createShowcasePerson(ctx, tx, actor, contact.Name)
						if err != nil {
							return fmt.Errorf("contact %s: %w", contact.Key, err)
						}
					}
					extra := map[string]string{"showcase_key": contact.Key}
					if principalID != "" {
						extra["showcase_principal_id"] = principalID
					}
					created, err := crm.InsertContact(ctx, tx, actor, orgIDs[org.Key], crm.ContactWrite{Name: contact.Name, ContactFields: crm.ContactFields{Email: contact.Email, Phone: contact.Phone, Role: contact.Role}}, extra)
					if err != nil {
						return fmt.Errorf("contact %s: %w", contact.Key, err)
					}
					if principalID != "" {
						if _, err := crm.BindContactPrincipal(ctx, tx, actor, created.ID, principalID); err != nil {
							return fmt.Errorf("contact %s: %w", contact.Key, err)
						}
					}
					contactIDs[contact.Key] = storedContact{ID: created.ID, PrincipalID: principalID, Name: contact.Name, Email: contact.Email}
					report.Contacts = append(report.Contacts, ContactItem{Key: contact.Key, Action: "create", Name: contact.Name, Principal: principal})
				}
			}
			for _, quote := range bundle.Quotes {
				existing, err := quotes.FindShowcaseKey(ctx, tx, "quote", quote.Key)
				if err != nil {
					return err
				}
				if existing != "" {
					row, err := quotes.ReadShowcaseQuote(ctx, tx, existing)
					if err != nil {
						return err
					}
					linked, err := quotes.ShowcaseHasPublicLink(ctx, tx, existing)
					if err != nil {
						return err
					}
					link := "none"
					if linked {
						link = "unchanged"
					}
					report.Quotes = append(report.Quotes, QuoteItem{Key: quote.Key, Action: "unchanged", Title: row.Title, State: row.State, Versions: row.Version, PublicLink: link, OfferNo: row.OfferNo, ID: row.ID})
					continue
				}
				link := "none"
				if quote.PublicLink {
					if linkKey == nil {
						link = "needs_link_key"
					} else {
						link = "create"
					}
				}
				if !apply {
					if err := planQuote(ctx, tx, bundle, settings, day, quote); err != nil {
						return fmt.Errorf("quote %s: %w", quote.Key, err)
					}
					report.Quotes = append(report.Quotes, QuoteItem{Key: quote.Key, Action: "create", Title: quote.Versions[len(quote.Versions)-1].Title, State: quote.State, Versions: plannedVersions(quote), PublicLink: link})
					continue
				}
				if quote.PublicLink && linkKey == nil {
					return fmt.Errorf("quote %s requests a public link but no host link key is configured (AEON_LINK_KEY_FILE or AEON_MESSAGING_KEY_FILE)", quote.Key)
				}
				item, err := writeQuote(ctx, tx, actor, bundle, settings, day, quote, orgIDs, contactIDs, linkKey)
				if err != nil {
					return fmt.Errorf("quote %s: %w", quote.Key, err)
				}
				item.PublicLink = link
				report.Quotes = append(report.Quotes, item)
			}
			return nil
		})
	})
	if err != nil {
		return report, err
	}
	sort.Slice(report.Organisations, func(i, j int) bool { return report.Organisations[i].Key < report.Organisations[j].Key })
	sort.Slice(report.Contacts, func(i, j int) bool { return report.Contacts[i].Key < report.Contacts[j].Key })
	sort.Slice(report.Profiles, func(i, j int) bool { return report.Profiles[i].Name < report.Profiles[j].Name })
	sort.Slice(report.Quotes, func(i, j int) bool { return report.Quotes[i].Key < report.Quotes[j].Key })
	return report, nil
}

func plannedVersions(quote QuoteSpec) int {
	if quote.State == "draft" {
		return 0
	}
	return len(quote.Versions)
}

func planQuote(ctx context.Context, tx pgx.Tx, bundle Bundle, settings quotes.ShowcaseSettings, day time.Time, quote QuoteSpec) error {
	if quote.ProfileName == "" && settings.DefaultProfileID == "" {
		return errors.New("quote settings have no default profile; run aeon quote-profile apply --default first")
	}
	if quote.ProfileName != "" && !profileInBundle(bundle, quote.ProfileName) {
		if _, err := quotes.ProfileIDByName(ctx, tx, quote.ProfileName); err != nil {
			return err
		}
	}
	org, contact, ok := quoteParties(bundle, quote)
	if !ok {
		return errors.New("organisation or contact is missing")
	}
	offer := day.Format("2006-01-02")
	until := day.AddDate(0, 0, quote.ValidityDays).Format("2006-01-02")
	final := quote.State != "draft"
	for _, version := range quote.Versions {
		if version.Currency != settings.Currency {
			return fmt.Errorf("currency %s does not match the tenant default %s", version.Currency, settings.Currency)
		}
		recipient := map[string]string{"name": org.Name, "address": addressLines(org.Billing), "contact": contact.Name, "country": org.Billing.Country, "customer_no": "K00000", "email": contact.Email, "contact_node_id": ""}
		if err := quotes.ValidateShowcaseDraft(settings.Sender, settings.Layout, settings.Currency, version, recipient, offer, until, final); err != nil {
			return err
		}
	}
	return nil
}

func profileInBundle(bundle Bundle, name string) bool {
	for _, profile := range bundle.Profiles {
		if profile.Name == name {
			return true
		}
	}
	return false
}

func quoteParties(bundle Bundle, quote QuoteSpec) (Organisation, Contact, bool) {
	for _, org := range bundle.Organisations {
		if org.Key != quote.Organisation {
			continue
		}
		for _, contact := range org.Contacts {
			if contact.Key == quote.Contact {
				return org, contact, true
			}
		}
	}
	return Organisation{}, Contact{}, false
}

func addressLines(address crm.Address) string {
	return strings.TrimSpace(address.Street + "\n" + strings.TrimSpace(address.PostalCode+" "+address.City))
}

func writeQuote(ctx context.Context, tx pgx.Tx, actor tenant.Principal, bundle Bundle, settings quotes.ShowcaseSettings, day time.Time, quote QuoteSpec, orgIDs map[string]string, contacts map[string]storedContact, linkKey []byte) (QuoteItem, error) {
	var item QuoteItem
	if err := planQuote(ctx, tx, bundle, settings, day, quote); err != nil {
		return item, err
	}
	if quote.ProfileName == "" && settings.DefaultProfileID == "" {
		return item, errors.New("quote settings have no default profile; run aeon quote-profile apply --default first")
	}
	profileID := ""
	if quote.ProfileName != "" {
		var err error
		profileID, err = quotes.ProfileIDByName(ctx, tx, quote.ProfileName)
		if err != nil {
			return item, err
		}
	}
	orgID := orgIDs[quote.Organisation]
	contact := contacts[quote.Contact]
	if orgID == "" || contact.ID == "" {
		return item, errors.New("organisation or contact was not created")
	}
	offer := day.Format("2006-01-02")
	until := day.AddDate(0, 0, quote.ValidityDays).Format("2006-01-02")
	created, err := quotes.CreateShowcaseQuote(ctx, tx, actor, quote.Versions[0].Title, orgID, profileID, quote.Key)
	if err != nil {
		return item, err
	}
	recipient := &quotes.ShowcaseRecipient{Name: contact.Name, Email: contact.Email, ID: contact.ID}
	if err := quotes.WriteShowcaseDraft(ctx, tx, actor, created.ID, quote.Versions[0], offer, until, settings.Currency, recipient); err != nil {
		return item, err
	}
	state := "draft"
	versions := 0
	if quote.State != "draft" {
		var issued quotes.ShowcaseQuote
		var digest string
		key := linkKey
		if !quote.PublicLink || len(quote.Versions) != 1 {
			key = nil
		}
		issued, digest, err = quotes.IssueShowcaseQuote(ctx, tx, actor, created.ID, key)
		if err != nil {
			return item, err
		}
		if len(quote.Versions) == 2 {
			if err := quotes.BranchShowcaseQuote(ctx, tx, actor, issued.ID, issued.Revision, issued.Version, digest); err != nil {
				return item, err
			}
			if err := quotes.WriteShowcaseDraft(ctx, tx, actor, issued.ID, quote.Versions[1], offer, until, settings.Currency, recipient); err != nil {
				return item, err
			}
			issued, digest, err = quotes.IssueShowcaseQuote(ctx, tx, actor, issued.ID, nil)
			if err != nil {
				return item, err
			}
		}
		state = issued.State
		versions = issued.Version
		if quote.State == "accepted" {
			if contact.PrincipalID == "" {
				return item, errors.New("acceptance principal is missing")
			}
			person := tenant.Principal{TenantID: actor.TenantID, ID: contact.PrincipalID, Kind: tenant.Person}
			if err := quotes.AcceptShowcaseQuote(ctx, tx, person, issued.ID, issued.Version, digest); err != nil {
				return item, err
			}
			state = "accepted"
		}
		created = issued
	}
	return QuoteItem{Key: quote.Key, Action: "create", Title: quote.Versions[len(quote.Versions)-1].Title, State: state, Versions: versions, OfferNo: created.OfferNo, ID: created.ID}, nil
}

// createShowcasePerson inserts the acceptance person and the same
// principal.created audit the other principal creation paths append.
// The event actor is the command actor, in the caller's transaction.
func createShowcasePerson(ctx context.Context, tx pgx.Tx, actor tenant.Principal, name string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,'{}') RETURNING id::text`, actor.TenantID, name).Scan(&id); err != nil {
		return "", err
	}
	_, err := events.Append(ctx, tx, actor, events.Change{Type: "principal.created", After: map[string]any{"id": id, "kind": "person", "name": name, "roles": []string{}}})
	return id, err
}

func senderReady(raw json.RawMessage) error {
	var sender struct {
		Company    string `json:"company"`
		Street     string `json:"street"`
		PostalCode string `json:"postal_code"`
		City       string `json:"city"`
		Country    string `json:"country"`
		Email      string `json:"email"`
	}
	if json.Unmarshal(raw, &sender) != nil || strings.TrimSpace(sender.Company) == "" || strings.TrimSpace(sender.Street) == "" || strings.TrimSpace(sender.PostalCode) == "" || strings.TrimSpace(sender.City) == "" || strings.TrimSpace(sender.Country) == "" {
		return errors.New("quote settings sender is incomplete: company, street, postal code, city, country and email are required")
	}
	parsed, err := mail.ParseAddress(sender.Email)
	if err != nil || parsed.Address != sender.Email {
		return errors.New("quote settings sender is incomplete: company, street, postal code, city, country and email are required")
	}
	return nil
}

func resolveActor(ctx context.Context, tx pgx.Tx, tenantID, actorID string, apply bool) (tenant.Principal, error) {
	actor := tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Person}
	if !apply {
		return actor, nil
	}
	if actorID == "" {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND 'operator'=ANY(roles) ORDER BY created_at,id LIMIT 1`).Scan(&actorID); err != nil {
			return actor, fmt.Errorf("operator actor unavailable; pass --actor-principal-id: %w", err)
		}
		actor.ID = actorID
	}
	var kind, status string
	var operator bool
	if err := tx.QueryRow(ctx, `SELECT kind,status,kind='agent' AND 'operator'=ANY(roles) FROM principals WHERE id=$1::uuid`, actorID).Scan(&kind, &status, &operator); err != nil {
		return actor, err
	}
	if status != "active" {
		return actor, errors.New("actor is inactive")
	}
	if kind == "agent" {
		actor.Kind = tenant.Agent
	}
	if !operator {
		if kind != "person" || authz.RequireTx(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Person}, "quotes.manage", authz.Scope{}) != nil {
			return actor, errors.New("actor requires quote management permission")
		}
	}
	return actor, nil
}
