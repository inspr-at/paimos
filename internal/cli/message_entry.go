// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/inspr-at/paimos/internal/version"
)

// RunMessaging is the shipped entry point for CP1. It preserves the existing
// command tree and replaces tell, listen, and message with messaging parity.
func RunMessaging(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runMessaging(args, stdin, stdout, stderr, nil)
}

func runMessaging(args []string, stdin io.Reader, stdout, stderr io.Writer, deliver localDeliverer) int {
	rt := &runtime{stdin: stdin, stdout: stdout, stderr: stderr, program: "aeon", messagingDeliverer: deliver}
	if len(args) > 0 {
		rt.program = programName(args[0])
	}
	err := rt.executeMessaging(args)
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		rt.printErr(ee)
		if ee.code != 0 {
			return ee.code
		}
		return 1
	}
	fmt.Fprintln(stderr, rt.program+": "+redact(err.Error(), ""))
	return 1
}
func (rt *runtime) executeMessaging(args []string) error {
	stop := rt.armCommandSignals()
	defer stop()
	root := rt.root()
	replacements := map[string]*Command{"tell": rt.cmdMessagingTell(), "listen": rt.cmdMessagingListen(), "message": rt.cmdMessaging()}
	for i, cmd := range root.subs {
		if replacement, ok := replacements[cmd.Name]; ok {
			root.subs[i] = replacement
		}
	}
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	cmd, cmdArgs, err := rt.walk(root, rest)
	if err != nil {
		return usagef("%s", err.Error())
	}
	pos, err := rt.parse(cmd, cmdArgs)
	if err != nil {
		return usagef("%s", err.Error())
	}
	if rt.help {
		rt.writeHelp(cmd)
		return nil
	}
	if rt.version {
		_, err := fmt.Fprintln(rt.stdout, version.Version)
		return err
	}
	if cmd.run == nil {
		if cmd.Name == "" {
			rt.writeHelp(cmd)
			return &exitError{code: 2}
		}
		return usagef("%s requires a subcommand", cmd.Name)
	}
	if err := cmd.checkArgs(pos); err != nil {
		return rt.noteInterrupt(err)
	}
	return rt.noteInterrupt(cmd.run(pos))
}
