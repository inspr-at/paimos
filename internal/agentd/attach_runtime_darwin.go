// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bytes"
	"encoding/binary"
	"strconv"

	"golang.org/x/sys/unix"
)

// kern.procargs2 is bounded so a padded buffer cannot hide a later Bun
// assignment inside a truncated scan. Oversized input fails closed.
const maxAttachProcargs = 1 << 20

type claudeRuntimeDecision int

const (
	claudeRuntimeAllow claudeRuntimeDecision = iota
	claudeRuntimeInjected
	claudeRuntimeUnobservable
)

func readAttachProcargs(pid string) (claudeRuntimeDecision, error) {
	n, err := strconv.Atoi(pid)
	if err != nil || n < 1 || strconv.Itoa(n) != pid {
		return 0, errAttachSignatureUnavailable
	}
	buf, err := unix.SysctlRaw("kern.procargs2", n)
	if err != nil {
		return 0, errAttachSignatureUnavailable
	}
	return scanAttachProcargs(buf)
}

// scanAttachProcargs decides from Darwin's KERN_PROCARGS2 buffer and does not
// retain the strings. The layout is argc, the executable path, NUL padding,
// argv, the environment, a terminating NUL, and the apple vector. Empty argv
// entries are NULs too, so they are indistinguishable from that padding. Every
// NUL-terminated string after the executable path is decided on its own, whether
// the argc walk would call it argv, the environment, or the apple vector. The
// argc walk only distinguishes a visible environment from an omitted one. A walk
// that runs out of bytes, a string with no closing NUL, or a buffer outside the
// size cap fails closed.
//
// Signed claude.exe can run caller JavaScript from a non-blank BUN_* assignment
// other than BUN_INSTALL and still report Claude's version. NODE_* assignments
// stay allowed. This is a best-effort deterrent. KERN_PROCARGS2 reflects the
// process's own rewritable string area; only argc comes from the kernel. Code
// in the process can rewrite that area before attach, including a NUL that ends
// the string list early. Reliable exec-time environment capture needs Endpoint
// Security (root and an entitlement) and is out of scope. The person's approval
// remains the real gate.
func scanAttachProcargs(buf []byte) (claudeRuntimeDecision, error) {
	if len(buf) < 4 || len(buf) > maxAttachProcargs {
		return 0, errAttachSignatureUnavailable
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	if argc < 1 || argc > 4096 {
		return 0, errAttachSignatureUnavailable
	}
	rest := buf[4:]
	pathEnd := bytes.IndexByte(rest, 0)
	if pathEnd < 1 {
		return 0, errAttachSignatureUnavailable
	}
	rest = rest[pathEnd+1:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	injected := false
	for n := 0; n < argc; n++ {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return 0, errAttachSignatureUnavailable
		}
		injected = injected || bunVariableDenied(rest[:i])
		rest = rest[i+1:]
	}
	if len(rest) == 0 {
		return claudeRuntimeFromScan(false, injected), nil
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return 0, errAttachSignatureUnavailable
		}
		if i > 0 {
			injected = injected || bunVariableDenied(rest[:i])
		}
		rest = rest[i+1:]
	}
	return claudeRuntimeFromScan(true, injected), nil
}

func claudeRuntimeFromScan(visible, injected bool) claudeRuntimeDecision {
	if injected {
		return claudeRuntimeInjected
	}
	if !visible {
		return claudeRuntimeUnobservable
	}
	return claudeRuntimeAllow
}

// bunVariableDenied reports a non-blank BUN_* assignment outside the explicit
// allowlist. The bytes are not copied or retained.
func bunVariableDenied(raw []byte) bool {
	key, value, ok := bytes.Cut(raw, []byte("="))
	if !ok || !bytes.HasPrefix(key, []byte("BUN_")) || bytes.Equal(key, []byte("BUN_INSTALL")) {
		return false
	}
	return len(bytes.TrimSpace(value)) != 0
}
