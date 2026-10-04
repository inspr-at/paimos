// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "fmt"

// exitError carries a process status. 2 is usage. 3 means the command stopped
// without writing: the action is not available yet, or a person has to finish
// it in the web app.
type exitError struct {
	code      int
	msg       string
	apiStatus int // Preserve HTTP classification without retaining unredacted errors.
}

func (e *exitError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &exitError{code: 2, msg: fmt.Sprintf(format, args...)}
}

func notYet(msg string) error {
	return &exitError{code: 3, msg: msg}
}
