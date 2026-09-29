// SPDX-License-Identifier: AGPL-3.0-only

// Package version exposes the build's calendar version (inspr-calver-3).
// The release pipeline injects Version via -ldflags; development builds report "dev".
package version

// Version is the canonical calendar version (YYMMDDhhmmss.0.0) or "dev".
var Version = "dev"

// Scheme is the machine version scheme identifier. Builds of this code carry
// versions reserved under inspr-calver-3 (INSPR-CalVer3, AEON-309); earlier
// releases stay inspr-calendar-v2 history.
const Scheme = "inspr-calver-3"
