// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

type ResidencyUnmet struct{}

func (*ResidencyUnmet) Error() string     { return "account is outside the allowed providers" }
func (*ResidencyUnmet) HTTPStatus() int   { return 409 }
func (*ResidencyUnmet) ErrorCode() string { return "residency_unmet" }
