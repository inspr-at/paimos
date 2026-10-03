// SPDX-License-Identifier: AGPL-3.0-only
package sessionusage

// BillingMode preserves only explicit known billing. Labels, model names,
// quota windows and token prices cannot establish how an account is billed.
func BillingMode(mode string) string {
	switch mode {
	case "api", "subscription":
		return mode
	}
	return "unknown"
}
