// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import "github.com/inspr-at/paimos/backend/mailer"

func SetReleaseAcceptanceMailerForTest(m mailer.Mailer) {
	releaseAcceptanceMail = m
}
