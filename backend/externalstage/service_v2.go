// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var releaseManifestCoordinatePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}:[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,189}$`)

// ReportV2 records a scheme-aware report without changing the frozen v1 DTO or
// its request digest. The complete normalized v2 body owns replay identity;
// the v1-compatible projection continues to feed the established authority and
// canonical delivery-state machinery.
func (s *Service) ReportV2(ctx context.Context, p Principal, handoffID, idempotencyKey string, secret []byte, req ReportRequestV2) (ReportReceipt, error) {
	core := req.v1()
	normalized, err := s.normalizeReport(core)
	if err != nil {
		return ReportReceipt{}, err
	}
	req.ObservedAt = normalized.ObservedAt
	req.JanusEvidence = normalized.JanusEvidence
	if req.PharosEvidence != nil {
		evidence := *req.PharosEvidence
		evidence.ObservedAt = normalized.PharosEvidence.ObservedAt
		req.PharosEvidence = &evidence
	}
	requestDigest, err := canonicalDigest(req)
	if err != nil {
		return ReportReceipt{}, err
	}
	var artifact *ArtifactEvidenceV2
	if req.PharosEvidence != nil {
		copy := req.PharosEvidence.Artifact
		artifact = &copy
	}
	return s.reportNormalized(ctx, p, handoffID, idempotencyKey, secret, normalized, requestDigest, ContractMajorV2, artifact)
}

func validateArtifactEvidenceV2(artifact ArtifactEvidenceV2, secret []byte) error {
	if !KnownVersionScheme(artifact.VersionScheme) {
		return ErrInvalid
	}
	if artifact.ReleaseSequence < 0 || !symbolPattern.MatchString(artifact.ReleaseChannel) ||
		!releaseManifestCoordinatePattern.MatchString(artifact.ReleaseManifestCoordinate) ||
		!versionPattern.MatchString(artifact.Version) || !commitPattern.MatchString(artifact.CommitDigest) {
		return ErrInvalid
	}
	if _, err := decodeWireDigest(artifact.Digest); err != nil {
		return ErrInvalid
	}
	if _, err := decodeWireDigest(artifact.ReleaseManifestDigest); err != nil {
		return ErrInvalid
	}
	if !ValidVersionForScheme(artifact.VersionScheme, artifact.Version) {
		return ErrInvalid
	}
	for _, value := range []string{
		string(artifact.VersionScheme), artifact.Version, artifact.ReleaseChannel, artifact.Digest,
		artifact.CommitDigest, artifact.ReleaseManifestCoordinate, artifact.ReleaseManifestDigest,
	} {
		if secretEcho(value, secret) {
			return ErrInvalid
		}
	}
	return nil
}

// KnownVersionScheme reports whether a scheme is explicitly recognised.
// Unknown, empty, or misspelled schemes fail closed.
func KnownVersionScheme(scheme VersionScheme) bool {
	return scheme == VersionSchemeLegacy || scheme == VersionSchemeINSPRCalendar || scheme == VersionSchemeINSPRCalendarV2
}

// ValidVersionForScheme validates the version spelling for its declared
// scheme. Legacy strings are accepted as opaque (versionPattern applies);
// calendar eras are validated against their exact grammar and real dates.
func ValidVersionForScheme(scheme VersionScheme, version string) bool {
	switch scheme {
	case VersionSchemeLegacy:
		return true
	case VersionSchemeINSPRCalendar:
		return validINSPRCalendarVersion(version)
	case VersionSchemeINSPRCalendarV2:
		return validINSPRCalendarV2Version(version)
	default:
		return false
	}
}

// validINSPRCalendarV2Version accepts exactly YYMMDDhhmmss.0.0 with a real UTC
// Gregorian timestamp: twelve fixed-width digits that never start with zero,
// then the literal constant 0.0. Shorter or longer majors, prerelease or build
// suffixes, and any non-zero MINOR/PATCH are rejected.
func validINSPRCalendarV2Version(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[1] != "0" || parts[2] != "0" {
		return false
	}
	stamp := parts[0]
	if len(stamp) != 12 || stamp[0] < '1' || stamp[0] > '9' {
		return false
	}
	for i := 0; i < len(stamp); i++ {
		if stamp[i] < '0' || stamp[i] > '9' {
			return false
		}
	}
	yearPart, _ := strconv.Atoi(stamp[0:2])
	month, _ := strconv.Atoi(stamp[2:4])
	day, _ := strconv.Atoi(stamp[4:6])
	hour, _ := strconv.Atoi(stamp[6:8])
	minute, _ := strconv.Atoi(stamp[8:10])
	second, _ := strconv.Atoi(stamp[10:12])
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59 {
		return false
	}
	year := 2000 + yearPart
	actual := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	return actual.Year() == year && int(actual.Month()) == month && actual.Day() == day &&
		actual.Hour() == hour && actual.Minute() == minute && actual.Second() == second
}

func validINSPRCalendarVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 && len(parts) != 6 {
		return false
	}
	for _, part := range parts {
		if len(part) != 2 || part[0] < '0' || part[0] > '9' || part[1] < '0' || part[1] > '9' {
			return false
		}
	}
	yearPart, _ := strconv.Atoi(parts[0])
	month, _ := strconv.Atoi(parts[1])
	day, _ := strconv.Atoi(parts[2])
	hour, minute, second := 0, 0, 0
	if len(parts) == 6 {
		hour, _ = strconv.Atoi(parts[3])
		minute, _ = strconv.Atoi(parts[4])
		second, _ = strconv.Atoi(parts[5])
	}
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59 {
		return false
	}
	year := 2000 + yearPart
	actual := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	return actual.Year() == year && int(actual.Month()) == month && actual.Day() == day &&
		actual.Hour() == hour && actual.Minute() == minute && actual.Second() == second
}
