// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

type ResidencyUnmet struct{}

func (*ResidencyUnmet) Error() string     { return "account is outside the allowed providers" }
func (*ResidencyUnmet) HTTPStatus() int   { return 409 }
func (*ResidencyUnmet) ErrorCode() string { return "residency_unmet" }

// ScopeTooLarge refuses an oversized atomic re-stamp before writing any run.
type ScopeTooLarge struct{}

func (*ScopeTooLarge) Error() string     { return "too_many_active_runs" }
func (*ScopeTooLarge) HTTPStatus() int   { return 413 }
func (*ScopeTooLarge) ErrorCode() string { return "too_many_active_runs" }
