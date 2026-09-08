//go:build ignore

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// The native 127.0.0.1 SMTP fixture lives in package mailer/smtptest so
// the production binary does not compile it. Tests import smtptest; this
// file is retained only as a pointer and is excluded from builds.

package mailer
