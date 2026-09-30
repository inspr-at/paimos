// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// kern.procargs2 is bounded so a padded environment cannot hide a later
// BUN_OPTIONS entry inside a truncated scan. Oversized input fails closed.
const maxAttachProcargs = 1 << 20

type attachObservedEnv struct {
	entries []string
	visible bool
}

type claudeRuntimeDecision int

const (
	claudeRuntimeAllow claudeRuntimeDecision = iota
	claudeRuntimeInjected
	claudeRuntimeUnobservable
)

func readAttachProcargs(pid string) (attachObservedEnv, error) {
	n, err := strconv.Atoi(pid)
	if err != nil || n < 1 || strconv.Itoa(n) != pid {
		return attachObservedEnv{}, errAttachSignatureUnavailable
	}
	buf, err := unix.SysctlRaw("kern.procargs2", n)
	if err != nil {
		return attachObservedEnv{}, errAttachSignatureUnavailable
	}
	return attachProcargsEnv(buf)
}

func attachProcargsEnv(buf []byte) (attachObservedEnv, error) {
	if len(buf) < 4 || len(buf) > maxAttachProcargs {
		return attachObservedEnv{}, errAttachSignatureUnavailable
	}
	_, env, visible, err := parseAttachProcargs(buf)
	if err != nil {
		return attachObservedEnv{}, errAttachSignatureUnavailable
	}
	return attachObservedEnv{entries: env, visible: visible}, nil
}

// Layout is Darwin's KERN_PROCARGS2: argc, executable path, padding, argv,
// then the environment, a terminating NUL, and the apple vector. Argv is
// parsed only so it is not mistaken for the environment. The apple vector is
// not environment. No bytes after argv means the kernel omitted it.
func parseAttachProcargs(buf []byte) (argv, env []string, envVisible bool, err error) {
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	if argc < 1 || argc > 4096 {
		return nil, nil, false, errAttachSignatureUnavailable
	}
	rest := buf[4:]
	pathEnd := bytes.IndexByte(rest, 0)
	if pathEnd < 1 {
		return nil, nil, false, errAttachSignatureUnavailable
	}
	rest = rest[pathEnd+1:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argv = make([]string, 0, argc)
	for n := 0; n < argc; n++ {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil, nil, false, errAttachSignatureUnavailable
		}
		argv = append(argv, string(rest[:i]))
		rest = rest[i+1:]
	}
	if len(rest) == 0 {
		return argv, nil, false, nil
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil, nil, false, errAttachSignatureUnavailable
		}
		if i == 0 {
			break
		}
		env = append(env, string(rest[:i]))
		rest = rest[i+1:]
	}
	return argv, env, true, nil
}

// Signed claude.exe runs caller JavaScript from a non-empty BUN_OPTIONS value
// and still reports Claude's version. That variable is visible for the vendor
// image. Platform binaries omit the environment, which is not evidence the
// variable is unset. Codex's signed image ignores the variable.
func decideClaudeRuntime(identifier string, observed attachObservedEnv) claudeRuntimeDecision {
	if identifier != attachVendorIdentifier(Claude) {
		return claudeRuntimeAllow
	}
	if !observed.visible {
		return claudeRuntimeUnobservable
	}
	for _, entry := range observed.entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "BUN_OPTIONS" && strings.TrimSpace(value) != "" {
			return claudeRuntimeInjected
		}
	}
	return claudeRuntimeAllow
}
