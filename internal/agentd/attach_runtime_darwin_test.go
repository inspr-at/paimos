// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestMain keeps a procargs fixture from running this package's tests.
// An argv of ["", "", "-test.run=…"] stops flag parsing at the second empty
// entry, so -test.run never selects the sleeping helper. The child marker is
// handled here, before m.Run, and the process stays inert until the parent kills it.
func TestMain(m *testing.M) {
	if os.Getenv("AEON_ATTACH_WRAPPER_CHILD") == "1" {
		if marker := os.Getenv("AEON_ATTACH_WRAPPER_MARKER"); marker != "" {
			if err := os.WriteFile(marker, []byte("inert"), 0o600); err != nil {
				os.Exit(1)
			}
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestScanAttachProcargs(t *testing.T) {
	path := "/bin/sleep"
	cleanEnv := []string{"PATH=/usr/bin:/bin", "NODE_OPTIONS=--require /fixture/marker.cjs", "NODE_EXTRA_CA_CERTS=/etc/ssl/cert.pem"}
	t.Run("empty-argv0", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{""}, []string{"BUN_OPTIONS=--preload /fixture/marker.cjs", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("empty-argv", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{"", "x"}, []string{"BUN_OPTIONS=--preload /fixture/marker.cjs", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("empty-argv-pair", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{"", "", "20"}, []string{"BUN_BE_BUN=1", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("argv-assignment", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "BUN_CONFIG_FILE=/fixture/bunfig.toml"}, cleanEnv, true)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("env-assignment", func(t *testing.T) {
		for _, value := range []string{"--preload /fixture/marker.cjs", "-r /fixture/marker.cjs", "--require /fixture/marker.cjs", "--config /fixture/bunfig.toml", "--smol", " --preload /fixture/marker.cjs"} {
			buf := procargsBuffer(t, path, []string{path, "20"}, []string{"BUN_OPTIONS=" + value, "FOO=BUN_OPTIONS=--preload /fixture/other.cjs"}, true)
			mustScan(t, buf, claudeRuntimeInjected)
		}
	})
	t.Run("apple-assignment", func(t *testing.T) {
		buf := withApple(procargsBuffer(t, path, []string{path, "20"}, []string{"PATH=/usr/bin:/bin"}, true), "BUN_OPTIONS=--preload /fixture/apple.cjs")
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("other-bun", func(t *testing.T) {
		for _, entry := range []string{"BUN_INSPECT_PRELOAD=/fixture/marker.cjs", "BUN_CONFIG_FILE=/fixture/bunfig.toml", "BUN_BE_BUN=1"} {
			buf := procargsBuffer(t, path, []string{path, "20"}, []string{entry, "PATH=/usr/bin:/bin"}, true)
			mustScan(t, buf, claudeRuntimeInjected)
		}
	})
	t.Run("allowlist", func(t *testing.T) {
		env := append(append([]string{}, cleanEnv...), "BUN_INSTALL=/opt/homebrew", "BUN_OPTIONS=", "BUN_OPTIONS=   ")
		mustScan(t, procargsBuffer(t, path, []string{path, "BUN_INSTALL=/opt/homebrew"}, env, true), claudeRuntimeAllow)
		mustScan(t, withApple(procargsBuffer(t, path, []string{path, "20"}, env, true), "BUN_INSTALL=/opt/homebrew"), claudeRuntimeAllow)
	})
	t.Run("allowlist-exact", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "20"}, []string{"BUN_INSTALL_BIN=/opt/homebrew/bin", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("node-prefix", func(t *testing.T) {
		mustScan(t, procargsBuffer(t, path, []string{path, "20"}, cleanEnv, true), claudeRuntimeAllow)
	})
	t.Run("blank", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "20"}, []string{"BUN_OPTIONS=", "BUN_OPTIONS=   ", "BUN_BE_BUN=", "BUN_INSTALL=", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeAllow)
	})
	t.Run("decoy", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "BUN_OPTIONS"}, []string{"FOO=BUN_OPTIONS=--preload /fixture/marker.cjs", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeAllow)
	})
	t.Run("argv-flags", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "--preload", "/fixture/marker.cjs", "-r", "/fixture/marker.cjs", "--config", "/fixture/bunfig.toml"}, cleanEnv, true)
		mustScan(t, buf, claudeRuntimeAllow)
	})
	t.Run("apple-harmless", func(t *testing.T) {
		buf := withApple(procargsBuffer(t, path, []string{path, "20"}, cleanEnv, true), "pfz=0x1")
		mustScan(t, buf, claudeRuntimeAllow)
	})
	t.Run("omitted", func(t *testing.T) {
		mustScan(t, procargsBuffer(t, path, []string{path, "30"}, nil, false), claudeRuntimeUnobservable)
	})
	t.Run("omitted-empty-argv", func(t *testing.T) {
		mustScanError(t, procargsBuffer(t, path, []string{"", ""}, nil, false))
	})
	t.Run("argv-injection-omitted-env", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "BUN_OPTIONS=--preload /fixture/marker.cjs"}, nil, false)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("dropped-terminator", func(t *testing.T) {
		full := procargsBuffer(t, path, []string{path, "20"}, []string{"BUN_OPTIONS=--preload /fixture/marker.cjs", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, full[:len(full)-1], claudeRuntimeInjected)
	})
	t.Run("truncated", func(t *testing.T) {
		full := procargsBuffer(t, path, []string{path, "20"}, []string{"PATH=/usr/bin:/bin", "NODE_OPTIONS=--require /fixture/marker.cjs"}, true)
		mustScanError(t, full[:len(full)-4])
	})
	t.Run("oversize", func(t *testing.T) {
		valid := procargsBuffer(t, path, []string{path, "20"}, cleanEnv, true)
		padded := append(append([]byte(nil), valid...), bytes.Repeat([]byte{0}, maxAttachProcargs)...)
		mustScanError(t, padded)
	})
	t.Run("short", func(t *testing.T) {
		mustScanError(t, nil)
		mustScanError(t, []byte{1, 2, 3})
	})
	t.Run("argc-cap", func(t *testing.T) {
		var buf bytes.Buffer
		if err := binary.Write(&buf, binary.LittleEndian, uint32(4097)); err != nil {
			t.Fatal(err)
		}
		buf.WriteString(path)
		buf.WriteByte(0)
		for i := 0; i < 4097; i++ {
			buf.WriteString("a")
			buf.WriteByte(0)
		}
		buf.WriteString("PATH=/usr/bin:/bin")
		buf.WriteByte(0)
		buf.WriteByte(0)
		mustScanError(t, buf.Bytes())
	})
	t.Run("path-ignored", func(t *testing.T) {
		buf := procargsBuffer(t, "BUN_OPTIONS=--preload /fixture/marker.cjs", []string{path, "20"}, cleanEnv, true)
		mustScan(t, buf, claudeRuntimeAllow)
	})
	t.Run("case-sensitivity", func(t *testing.T) {
		for _, entry := range []string{"bun_options=--preload /fixture/marker.cjs", "Bun_OPTIONS=--preload /fixture/marker.cjs"} {
			buf := procargsBuffer(t, path, []string{path, "20"}, []string{entry, "PATH=/usr/bin:/bin"}, true)
			mustScan(t, buf, claudeRuntimeAllow)
		}
	})
	t.Run("bun-underscore", func(t *testing.T) {
		buf := procargsBuffer(t, path, []string{path, "20"}, []string{"BUN_=x", "PATH=/usr/bin:/bin"}, true)
		mustScan(t, buf, claudeRuntimeInjected)
	})
	t.Run("non-utf8", func(t *testing.T) {
		denied := []string{
			"BUN_OPTIONS=" + string([]byte{0xff, 0xfe}),
			"BUN_" + string([]byte{0xff}) + "=x",
		}
		for _, entry := range denied {
			buf := procargsBuffer(t, path, []string{path, "20"}, []string{entry, "PATH=/usr/bin:/bin"}, true)
			mustScan(t, buf, claudeRuntimeInjected)
		}
		allowed := []string{
			string([]byte{0xff, 0xfe}) + "=--preload /fixture/marker.cjs",
			string([]byte{0xff, 0xfe, 0xfd}),
		}
		for _, entry := range allowed {
			buf := procargsBuffer(t, path, []string{path, "20"}, []string{entry, "PATH=/usr/bin:/bin"}, true)
			mustScan(t, buf, claudeRuntimeAllow)
		}
	})
	t.Run("exact-cap", func(t *testing.T) {
		mustScan(t, sizedProcargs(t, maxAttachProcargs, true), claudeRuntimeInjected)
	})
	t.Run("cap-plus-one", func(t *testing.T) {
		mustScanError(t, sizedProcargs(t, maxAttachProcargs+1, true))
	})
}

func mustScan(t *testing.T, buf []byte, want claudeRuntimeDecision) {
	t.Helper()
	got, err := scanAttachProcargs(buf)
	if err != nil || got != want {
		t.Fatalf("scan decision %v err %v", got, err)
	}
}

func mustScanError(t *testing.T, buf []byte) {
	t.Helper()
	if _, err := scanAttachProcargs(buf); err == nil {
		t.Fatal("ambiguous procargs accepted")
	}
}

func procargsBuffer(t *testing.T, path string, argv, env []string, terminate bool) []byte {
	t.Helper()
	if path == "" || len(argv) == 0 {
		t.Fatal("procargs fixture needs a path and argv")
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(argv))); err != nil {
		t.Fatal(err)
	}
	buf.WriteString(path)
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

// sizedProcargs builds a valid KERN_PROCARGS2 buffer of exactly size bytes.
// When denied is set, the last environment string is a Bun assignment, so a
// successful scan has walked the whole buffer rather than stopping at the cap.
func sizedProcargs(t *testing.T, size int, denied bool) []byte {
	t.Helper()
	path := "/bin/sleep"
	prefix := procargsBuffer(t, path, []string{path, "20"}, nil, false)
	suffix := []byte{0}
	if denied {
		suffix = append([]byte("BUN_OPTIONS=x\x00"), 0)
	}
	overhead := len(prefix) + len("P=") + 1 + len(suffix)
	if overhead > size {
		t.Fatalf("procargs overhead %d exceeds %d", overhead, size)
	}
	buf := make([]byte, 0, size)
	buf = append(buf, prefix...)
	buf = append(buf, 'P', '=')
	buf = append(buf, bytes.Repeat([]byte{'A'}, size-overhead)...)
	buf = append(buf, 0)
	buf = append(buf, suffix...)
	if len(buf) != size {
		t.Fatalf("procargs size %d, want %d", len(buf), size)
	}
	return buf
}

func withApple(buf []byte, entries ...string) []byte {
	out := append([]byte(nil), buf...)
	for _, entry := range entries {
		out = append(out, entry...)
		out = append(out, 0)
	}
	return out
}

func TestAttachProcargsReadsFixtureEnvironment(t *testing.T) {
	denied := startProcargsFixture(t, []string{"PATH=/usr/bin:/bin", "BUN_OPTIONS=--preload /fixture/marker.cjs", "FOO=BUN_OPTIONS=--preload /fixture/other.cjs", "NODE_OPTIONS=--require /fixture/marker.cjs"})
	allowed := startProcargsFixture(t, []string{"PATH=/usr/bin:/bin", "BUN_INSTALL=/opt/homebrew", "BUN_OPTIONS=", "NODE_EXTRA_CA_CERTS=/etc/ssl/cert.pem", "NODE_OPTIONS=--require /fixture/marker.cjs"})
	other := startProcargsFixture(t, []string{"PATH=/usr/bin:/bin", "BUN_BE_BUN=1"})
	empty := startProcargsArgvFixture(t, []string{"", "-test.run=^TestAttachExecWrapperChild$"}, []string{"PATH=/usr/bin:/bin", "BUN_OPTIONS=--preload /fixture/marker.cjs"})
	pair := startProcargsArgvFixture(t, []string{"", "", "-test.run=^TestAttachExecWrapperChild$"}, []string{"PATH=/usr/bin:/bin", "BUN_BE_BUN=1"})
	if decision, err := readAttachProcargs(denied); err != nil || decision != claudeRuntimeInjected {
		t.Fatal("live Claude BUN_OPTIONS was not refused", err)
	}
	if decision, err := readAttachProcargs(allowed); err != nil || decision != claudeRuntimeAllow {
		t.Fatal("allowlisted live environment was refused", err)
	}
	if decision, err := readAttachProcargs(other); err != nil || decision != claudeRuntimeInjected {
		t.Fatal("live BUN_BE_BUN was not refused", err)
	}
	if decision, err := readAttachProcargs(empty); err != nil || decision != claudeRuntimeInjected {
		t.Fatal("live empty argv0 hid BUN_OPTIONS", err)
	}
	if decision, err := readAttachProcargs(pair); err != nil || decision != claudeRuntimeInjected {
		t.Fatal("live empty argv pair hid BUN_BE_BUN", err)
	}
	if _, err := readAttachProcargs("0"); err == nil {
		t.Fatal("invalid pid reached procargs")
	}
	omitted := startOmittedProcargsFixture(t)
	if decision, err := readAttachProcargs(omitted); err != nil || decision != claudeRuntimeUnobservable {
		t.Fatal("omitted live environment counted as a clean Claude process", err)
	}
}

func TestAttachProcargsEmptyArgvStaysInert(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "marker")
	cmd := exec.Command(self)
	cmd.Args = []string{"", "", "-test.run=^TestAttachExecWrapperChild$"}
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"BUN_BE_BUN=1",
		"HOME=" + t.TempDir(),
		"AEON_ATTACH_WRAPPER_CHILD=1",
		"AEON_ATTACH_WRAPPER_MARKER=" + marker,
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var body []byte
	for {
		select {
		case err := <-exited:
			t.Fatal("empty argv fixture exited instead of staying inert", err)
		default:
		}
		body, err = os.ReadFile(marker)
		if err == nil && string(body) == "inert" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("empty argv fixture ran the suite instead of dispatching the inert marker", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-exited:
		t.Fatal("inert fixture exited", err)
	default:
	}
	if decision, err := readAttachProcargs(strconv.Itoa(cmd.Process.Pid)); err != nil || decision != claudeRuntimeInjected {
		t.Fatal("inert empty argv fixture hid BUN_BE_BUN", err)
	}
}

func startProcargsFixture(t *testing.T, env []string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return startProcargsArgvFixture(t, []string{self, "-test.run=^TestAttachExecWrapperChild$"}, env)
}

func startProcargsArgvFixture(t *testing.T, argv, env []string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Args = append([]string{}, argv...)
	cmd.Env = append(append([]string{}, env...), "HOME="+t.TempDir(), "AEON_ATTACH_WRAPPER_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return strconv.Itoa(cmd.Process.Pid)
}

func startOmittedProcargsFixture(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "BUN_OPTIONS=--preload /fixture/marker.cjs"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return strconv.Itoa(cmd.Process.Pid)
}
