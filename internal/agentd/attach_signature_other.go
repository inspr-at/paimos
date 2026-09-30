// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin

package agentd

import "context"

func inspectAttachSignature(context.Context, string) (attachSignature, error) {
	return attachSignature{}, nil
}
