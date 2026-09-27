// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin

package grokprobe

import (
	"context"
	"errors"
)

func Probe(context.Context, Binding) (Identity, error) {
	return Identity{}, errors.New("native Grok guided setup requires macOS")
}
