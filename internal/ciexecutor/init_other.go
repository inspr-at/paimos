// SPDX-License-Identifier: AGPL-3.0-only
//go:build !linux

package ciexecutor

import "fmt"

func Init() error { return fmt.Errorf("CI executor requires a disposable Linux guest") }
