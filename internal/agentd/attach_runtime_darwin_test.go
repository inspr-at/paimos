// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestParseAttachProcargsSeparatesArgvAndEnvironment(t *testing.T) {
	claude := attachVendorIdentifier(Claude)
	injected := procargsBuffer(t, []string{"/bin/sleep", "20"}, []string{"BUN_OPTIONS=--preload /fixture/marker.cjs", "FOO=BUN_OPTIONS=--preload /fixture/other.cjs"}, true)
	argv, env, visible, err := parseAttachProcargs(injected)
	if err != nil || !visible || len(argv) != 2 || argv[0] != "/bin/sleep" || argv[1] != "20" || decideClaudeRuntime(claude, attachObservedEnv{entries: env, visible: visible}) != claudeRuntimeInjected {
		t.Fatal("environment injection was not separated from argv", err)
	}
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "BUN_OPTIONS=") {
			kept = append(kept, entry)
		}
	}
	if decideClaudeRuntime(claude, attachObservedEnv{entries: kept, visible: true}) != claudeRuntimeAllow {
		t.Fatal("decoy value counted as BUN_OPTIONS")
	}
	argvOnly := procargsBuffer(t, []string{"/bin/sleep", "--preload", "/fixture/marker.cjs", "-r", "/fixture/marker.cjs", "--config", "/fixture/bunfig.toml"}, []string{"NODE_OPTIONS=--require /fixture/marker.cjs"}, true)
	argv, env, visible, err = parseAttachProcargs(argvOnly)
	observed := attachObservedEnv{entries: env, visible: visible}
	if err != nil || !visible || len(argv) != 7 || decideClaudeRuntime(claude, observed) != claudeRuntimeAllow || decideClaudeRuntime(attachVendorIdentifier(Codex), observed) != claudeRuntimeAllow {
		t.Fatal("argv flags were treated as a signed-runtime injection", err)
	}
	apple := append([]byte(nil), injected...)
	apple = append(apple, []byte("BUN_OPTIONS=--preload /fixture/apple.cjs\x00")...)
	_, env, visible, err = parseAttachProcargs(apple)
	if err != nil || !visible || len(env) != 2 {
		t.Fatal("environment terminator was discarded", err)
	}
	_, appleEnv, appleVisible, err := parseAttachProcargs(append(procargsBuffer(t, []string{"/bin/sleep", "20"}, []string{"PATH=/usr/bin:/bin"}, true), []byte("BUN_OPTIONS=--preload /fixture/apple.cjs\x00")...))
	if err != nil || !appleVisible || decideClaudeRuntime(claude, attachObservedEnv{entries: appleEnv, visible: appleVisible}) != claudeRuntimeAllow {
		t.Fatal("apple vector was read as the environment", err)
	}
	if _, _, _, err = parseAttachProcargs(injected[:len(injected)-4]); err == nil {
		t.Fatal("truncated environment accepted")
	}
	_, droppedTerminator, droppedVisible, err := parseAttachProcargs(injected[:len(injected)-1])
	if err != nil || !droppedVisible || decideClaudeRuntime(claude, attachObservedEnv{entries: droppedTerminator, visible: droppedVisible}) != claudeRuntimeInjected {
		t.Fatal("complete environment entries were rejected", err)
	}
	omitted := procargsBuffer(t, []string{"/bin/sleep", "30"}, nil, false)
	_, omittedEnv, omittedVisible, err := parseAttachProcargs(omitted)
	omittedObserved := attachObservedEnv{entries: omittedEnv, visible: omittedVisible}
	if err != nil || omittedVisible || len(omittedEnv) != 0 || decideClaudeRuntime(claude, omittedObserved) != claudeRuntimeUnobservable || decideClaudeRuntime(attachVendorIdentifier(Codex), omittedObserved) != claudeRuntimeAllow {
		t.Fatal("omitted environment counted as a clean Claude process", err)
	}
	if _, err = attachProcargsEnv(make([]byte, maxAttachProcargs+1)); err == nil {
		t.Fatal("oversize procargs accepted")
	}
}

func procargsBuffer(t *testing.T, argv, env []string, terminate bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(argv))); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("/bin/sleep")
	buf.WriteByte(0)
	buf.Write(bytes.Repeat([]byte{0}, 7))
	for _, arg := range argv {
		buf.WriteString(arg)
		buf.WriteByte(0)
	}
	for _, entry := range env {
		buf.WriteString(entry)
		buf.WriteByte(0)
	}
	if terminate {
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

func TestClaudeBunOptionsPolicy(t *testing.T) {
	claude := attachVendorIdentifier(Claude)
	codex := attachVendorIdentifier(Codex)
	for _, value := range []string{"--preload /fixture/marker.cjs", "-r /fixture/marker.cjs", "--require /fixture/marker.cjs", "--config /fixture/bunfig.toml", "--smol", " --preload /fixture/marker.cjs"} {
		if decideClaudeRuntime(claude, attachObservedEnv{entries: []string{"BUN_OPTIONS=" + value}, visible: true}) != claudeRuntimeInjected {
			t.Fatal("non-empty BUN_OPTIONS accepted")
		}
	}
	for _, env := range [][]string{
		nil,
		{"BUN_OPTIONS="},
		{"BUN_OPTIONS=   "},
		{"NODE_OPTIONS=--require /fixture/marker.cjs"},
		{"FOO=BUN_OPTIONS=--preload /fixture/marker.cjs"},
		{"BUN_INSPECT_PRELOAD=/fixture/marker.cjs"},
		{"BUN_CONFIG_FILE=/fixture/bunfig.toml"},
		{"BUN_BE_BUN=1"},
	} {
		if decideClaudeRuntime(claude, attachObservedEnv{entries: env, visible: true}) != claudeRuntimeAllow || decideClaudeRuntime(codex, attachObservedEnv{entries: []string{"BUN_OPTIONS=--preload /fixture/marker.cjs"}, visible: true}) != claudeRuntimeAllow {
			t.Fatal("benign or non-Claude environment refused")
		}
	}
	hidden := attachObservedEnv{}
	if decideClaudeRuntime(claude, hidden) != claudeRuntimeUnobservable || decideClaudeRuntime(codex, hidden) != claudeRuntimeAllow {
		t.Fatal("hidden environment was treated as a clean Claude process")
	}
}

func TestAttachProcargsReadsFixtureEnvironment(t *testing.T) {
	claude := attachVendorIdentifier(Claude)
	codex := attachVendorIdentifier(Codex)
	denied := startProcargsFixture(t, []string{"PATH=/usr/bin:/bin", "BUN_OPTIONS=--preload /fixture/marker.cjs", "FOO=BUN_OPTIONS=--preload /fixture/other.cjs", "NODE_OPTIONS=--require /fixture/marker.cjs"})
	allowed := startProcargsFixture(t, []string{"PATH=/usr/bin:/bin", "BUN_OPTIONS=", "NODE_OPTIONS=--require /fixture/marker.cjs"})
	deniedEnv, err := readAttachProcargs(denied)
	if err != nil || !deniedEnv.visible || decideClaudeRuntime(claude, deniedEnv) != claudeRuntimeInjected || decideClaudeRuntime(codex, deniedEnv) != claudeRuntimeAllow {
		t.Fatal("live Claude BUN_OPTIONS was not refused", err)
	}
	allowedEnv, err := readAttachProcargs(allowed)
	if err != nil || !allowedEnv.visible || decideClaudeRuntime(claude, allowedEnv) != claudeRuntimeAllow {
		t.Fatal("empty BUN_OPTIONS was refused", err)
	}
	if _, err = readAttachProcargs("0"); err == nil {
		t.Fatal("invalid pid reached procargs")
	}
	omitted := startOmittedProcargsFixture(t)
	omittedEnv, err := readAttachProcargs(omitted)
	if err != nil || omittedEnv.visible || decideClaudeRuntime(claude, omittedEnv) != claudeRuntimeUnobservable || decideClaudeRuntime(codex, omittedEnv) != claudeRuntimeAllow {
		t.Fatal("omitted live environment counted as a clean Claude process", err)
	}
}

func startProcargsFixture(t *testing.T, env []string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestAttachExecWrapperChild$")
	cmd.Env = append(append([]string{}, env...), "AEON_ATTACH_WRAPPER_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return strconv.Itoa(cmd.Process.Pid)
}

func startOmittedProcargsFixture(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "BUN_OPTIONS=--preload /fixture/marker.cjs"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return strconv.Itoa(cmd.Process.Pid)
}
