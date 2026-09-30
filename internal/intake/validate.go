// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hoursPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,7})(?:\.[0-9]{1,2})?$`)
)

func validateSource(in *sourceWrite) error {
	switch in.Kind {
	case "url", "file", "note", "conversation":
	default:
		return fail(http.StatusBadRequest, "invalid kind")
	}
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" || utf8.RuneCountInString(in.Label) > 512 || strings.ContainsRune(in.Label, 0) {
		return fail(http.StatusBadRequest, "invalid label")
	}
	locator, err := cleanLocator(in.Locator)
	if err != nil {
		return err
	}
	in.Locator = locator
	if !shaPattern.MatchString(in.ContentSHA256) {
		return fail(http.StatusBadRequest, "invalid content digest")
	}
	if err := cleanKey(&in.IdempotencyKey); err != nil {
		return err
	}
	fileID, err := cleanUUID(in.FileID, "invalid file id")
	if err != nil {
		return err
	}
	in.FileID = fileID
	if (in.Kind == "file") != (in.FileID != nil) {
		return fail(http.StatusBadRequest, "file sources require a file id")
	}
	return nil
}

func validateTurn(in *turnWrite) error {
	sourceID, err := cleanUUID(&in.SourceID, "invalid source id")
	if err != nil || sourceID == nil {
		return fail(http.StatusBadRequest, "invalid source id")
	}
	in.SourceID = *sourceID
	if in.Ordinal == nil || *in.Ordinal < 0 {
		return fail(http.StatusBadRequest, "invalid ordinal")
	}
	switch in.Speaker {
	case "person", "agent":
	default:
		return fail(http.StatusBadRequest, "invalid speaker")
	}
	speakerID, err := cleanUUID(in.SpeakerPrincipalID, "invalid speaker principal")
	if err != nil {
		return err
	}
	in.SpeakerPrincipalID = speakerID
	if in.Body == "" || utf8.RuneCountInString(in.Body) > 65536 || strings.ContainsRune(in.Body, 0) {
		return fail(http.StatusBadRequest, "invalid body")
	}
	return cleanKey(&in.IdempotencyKey)
}

func validateDraft(in *draftWrite) error {
	switch in.Kind {
	case "brief", "requirement":
	default:
		return fail(http.StatusBadRequest, "invalid kind")
	}
	if in.Kind == "requirement" {
		if in.RequirementKind == nil || (*in.RequirementKind != "functional" && *in.RequirementKind != "nonfunctional") {
			return fail(http.StatusBadRequest, "invalid requirement kind")
		}
	} else if in.RequirementKind != nil {
		return fail(http.StatusBadRequest, "briefs have no requirement kind")
	}
	target, err := cleanUUID(in.TargetNodeID, "invalid target")
	if err != nil {
		return err
	}
	in.TargetNodeID = target
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 512 || strings.ContainsRune(in.Title, 0) {
		return fail(http.StatusBadRequest, "invalid title")
	}
	if utf8.RuneCountInString(in.Body) > 65536 || strings.ContainsRune(in.Body, 0) {
		return fail(http.StatusBadRequest, "invalid body")
	}
	if in.BaseEventID == nil || *in.BaseEventID < 0 {
		return fail(http.StatusBadRequest, "invalid base event")
	}
	if (in.TargetNodeID == nil) != (*in.BaseEventID == 0) {
		return fail(http.StatusBadRequest, "a new draft uses base event 0 and an existing target uses its latest event")
	}
	if len(in.Citations) == 0 {
		return fail(http.StatusBadRequest, "a draft needs a citation")
	}
	for i := range in.Citations {
		if err := validateCitation(&in.Citations[i]); err != nil {
			return err
		}
	}
	if len(in.Suggestions) > 0 && (in.Kind != "requirement" || in.RequirementKind == nil || *in.RequirementKind != "functional") {
		return fail(http.StatusBadRequest, "only a functional requirement may suggest tickets")
	}
	for i := range in.Suggestions {
		if err := validateSuggestion(&in.Suggestions[i]); err != nil {
			return err
		}
	}
	if err := validateExtensions(in); err != nil {
		return err
	}
	return cleanKey(&in.IdempotencyKey)
}

func validateCitation(in *citationWrite) error {
	sourceID, err := cleanUUID(&in.SourceID, "invalid citation source")
	if err != nil || sourceID == nil {
		return fail(http.StatusBadRequest, "invalid citation source")
	}
	in.SourceID = *sourceID
	turnID, err := cleanUUID(in.TurnID, "invalid citation turn")
	if err != nil {
		return err
	}
	in.TurnID = turnID
	in.Locator = strings.TrimSpace(in.Locator)
	if in.Locator == "" || utf8.RuneCountInString(in.Locator) > 256 || strings.ContainsRune(in.Locator, 0) {
		return fail(http.StatusBadRequest, "invalid citation locator")
	}
	quote, err := cleanSHA(in.QuoteSHA256, "invalid citation digest")
	if err != nil {
		return err
	}
	in.QuoteSHA256 = quote
	return nil
}

func validateSuggestion(in *suggestionWrite) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 512 || strings.ContainsRune(in.Title, 0) {
		return fail(http.StatusBadRequest, "invalid suggestion title")
	}
	if !hoursPattern.MatchString(in.EstimatedHours.String()) {
		return fail(http.StatusBadRequest, "invalid estimate")
	}
	if in.Later == nil || in.AccessChange == nil {
		return fail(http.StatusBadRequest, "suggestion flags are required")
	}
	return nil
}

func cleanLocator(in *string) (*string, error) {
	if in == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*in)
	if value == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(value) > 2048 || strings.ContainsRune(value, 0) || locatorHasCredential(value) {
		return nil, fail(http.StatusBadRequest, "locator must be a public or symbolic reference")
	}
	return &value, nil
}

func locatorHasCredential(value string) bool {
	lower := strings.ToLower(value)
	if strings.ContainsAny(value, "\r\n") || strings.HasPrefix(lower, "data:") || strings.Contains(lower, "bearer ") {
		return true
	}
	if strings.Contains(lower, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil {
			return true
		}
	}
	if at := strings.IndexByte(value, '@'); at > 0 && strings.Contains(value[:at], ":") {
		return true
	}
	for _, needle := range []string{
		"password=", "passwd=", "secret=", "token=", "api_key=", "apikey=",
		"access_token=", "authorization=", "credential=",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func cleanKey(key *string) error {
	*key = strings.TrimSpace(*key)
	if *key == "" || len(*key) > 128 || strings.ContainsRune(*key, 0) || strings.ContainsAny(*key, "\r\n") {
		return fail(http.StatusBadRequest, "invalid idempotency key")
	}
	return nil
}

func cleanUUID(in *string, msg string) (*string, error) {
	if in == nil {
		return nil, nil
	}
	value := strings.ToLower(strings.TrimSpace(*in))
	if value == "" {
		return nil, nil
	}
	if !uuidPattern.MatchString(value) {
		return nil, fail(http.StatusBadRequest, msg)
	}
	return &value, nil
}

func cleanSHA(in *string, msg string) (*string, error) {
	if in == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*in)
	if value == "" {
		return nil, nil
	}
	if !shaPattern.MatchString(value) {
		return nil, fail(http.StatusBadRequest, msg)
	}
	return &value, nil
}

func sameString(a, b *string) bool {
	if a == nil || *a == "" {
		return b == nil || *b == ""
	}
	return b != nil && *a == *b
}
