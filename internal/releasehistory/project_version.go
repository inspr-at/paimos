// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"regexp"
	"time"
)

var legacyVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
var calendarV1 = regexp.MustCompile(`^[1-9][0-9]\.[0-9]{2}\.[0-9]{2}(?:\.[0-9]{2}\.[0-9]{2}\.[0-9]{2})?$`)

// ValidProjectVersion is explicitly scheme-tagged; adoption never silently
// migrates a legacy or calendar-v1 project to the product's calendar scheme.
func ValidProjectVersion(scheme, version string) bool {
	if len(scheme) > 64 || len(version) > 64 {
		return false
	}
	switch scheme {
	case "legacy":
		return legacyVersion.MatchString(version)
	case "inspr-calendar-v1":
		if !calendarV1.MatchString(version) {
			return false
		}
		layout := "2006.01.02"
		if len(version) == 17 {
			layout += ".15.04.05"
		}
		_, err := time.Parse(layout, "20"+version)
		return err == nil
	case "inspr-calendar-v2", "inspr-calver-3":
		return ValidVersion(version)
	}
	return false
}

var capturedNoteKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$`)

// ValidCapturedNoteKey is node-key validation, independent of commit reference parsing.
func ValidCapturedNoteKey(key string) bool { return len(key) <= 30 && capturedNoteKey.MatchString(key) }
