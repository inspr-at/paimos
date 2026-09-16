//go:build darwin

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

func TestGrokConversationPinnedAssetsAndEmptyToolArtifact(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config.toml")
	profile := filepath.Join(root, "conversation.txt")
	if err := writePinnedGrokAsset(config, grokConversationConfig, grokConfigSHA256); err != nil {
		t.Fatal(err)
	}
	if err := writePinnedGrokAsset(profile, grokConversationProfile, grokProfileSHA256); err != nil {
		t.Fatal(err)
	}
	if err := verifyGrokAssets(config, profile); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "tool_definitions.json")
	if grokFunctionToolsEmpty(root) {
		t.Fatal("missing artifact accepted")
	}
	if err := os.WriteFile(artifact, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if !grokFunctionToolsEmpty(root) {
		t.Fatal("empty function tools rejected")
	}
	if err := os.WriteFile(artifact, []byte(`[{"name":"read_file"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if grokFunctionToolsEmpty(root) {
		t.Fatal("active function tool accepted")
	}
	if err := os.WriteFile(config, []byte("[features]\nbackend_tools=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if verifyGrokAssets(config, profile) == nil {
		t.Fatal("config drift accepted")
	}
}

func TestGrokConversationPackageReadBoundary(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME unavailable")
	}
	packageRoot := filepath.Join(home, ".npm-global", "lib", "node_modules", "@xai-official", "grok")
	binary := filepath.Join(packageRoot, "bin", "grok-native")
	auth := filepath.Join(home, ".grok", "auth.json")
	scratch := filepath.Join(home, "Library", "Application Support", "paimos", "conversation-scratch")
	if got, err := grokPackageRoot(binary, auth, scratch); err != nil || got != packageRoot {
		t.Fatalf("package boundary=%q err=%v", got, err)
	}
	if _, err := grokPackageRoot(filepath.Join(home, "bin", "grok-native"), auth, scratch); err == nil {
		t.Fatal("home-wide binary parent accepted")
	}
	if _, err := grokPackageRoot(binary, filepath.Join(packageRoot, "auth.json"), scratch); err == nil {
		t.Fatal("auth inside binary read root accepted")
	}
	if _, err := grokPackageRoot(binary, auth, filepath.Join(packageRoot, "scratch")); err == nil {
		t.Fatal("scratch inside binary read root accepted")
	}
	profile, err := grokSeatbeltProfile(scratch, binary, auth, 61973)
	if err != nil || strings.Contains(profile, `(subpath "`+home+`")`) || !strings.Contains(profile, `(deny network-outbound)`) {
		t.Fatal("Seatbelt widened or missing network deny")
	}
}

func TestGrokConversationAuthScopeAndFileSafety(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "auth.json")
	subject := "synthetic-subject"
	digest := sha256.Sum256([]byte(subject))
	principal := hex.EncodeToString(digest[:])
	content := fmt.Sprintf(`{"https://auth.x.ai::client":{"auth_mode":"oidc","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client","principal_type":"User","user_id":%q,"key":"synthetic-bearer","expires_at":%q}}`, subject, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	before, bearer, got, err := readGrokAuth(path, principal)
	if err != nil || got != subject || bearer != "synthetic-bearer" {
		t.Fatalf("synthetic OIDC scope rejected: err=%v match=%v bearer_match=%v", err, got == subject, bearer == "synthetic-bearer")
	}
	if err := verifyGrokAuthUnchanged(path, principal, before); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readGrokAuth(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong principal accepted")
	}
	team := strings.Replace(content, `"principal_type":"User"`, `"principal_type":"Team"`, 1)
	if err := os.WriteFile(path, []byte(team), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readGrokAuth(path, principal); err == nil {
		t.Fatal("team principal accepted")
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readGrokAuth(link, principal); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readGrokAuth(path, principal); err == nil {
		t.Fatal("group/world readable auth accepted")
	}
}

func TestGrokConversationProxyDeniesUnlistedHostWithoutDialing(t *testing.T) {
	proxy, err := startGrokProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.stop()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxy.port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("CONNECT other.example:443 HTTP/1.1\r\nHost: other.example:443\r\n\r\n"))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, "403 Forbidden") || !proxy.violation.Load() {
		t.Fatalf("line=%q err=%v violation=%v", line, err, proxy.violation.Load())
	}
}

func TestGrokConversationOwnedRevocationAndDeadline(t *testing.T) {
	for _, test := range []struct {
		name   string
		reason ConversationFailure
	}{{"revoked", ConversationFailureCancelled}, {"deadline", ConversationFailureDeadline}} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestGrokConversationHelperProcess$")
			cmd.Env = append(os.Environ(), "PAIMOS_GROK_TEST_HELPER=1")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			configured := ownedprocess.Configure(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := ownedprocess.Verify(cmd, configured); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			p := &grokConversationProcess{cmd: cmd, wire: newGrokACPWire(stdout, stdin), stdin: stdin, sessionID: "owned-session", turnID: "turn-1", done: make(chan struct{})}
			go p.run(context.Background(), "synthetic", GrokConversationOptions{MaxOutputBytes: 32, MaxEvents: 8})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var result CodexConversationResult
			if test.name == "revoked" {
				if err := p.Stop(ctx); err != nil {
					t.Fatal(err)
				}
				result, err = p.WaitConversation(ctx)
			} else {
				deadline, cancelDeadline := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancelDeadline()
				result, err = p.WaitConversation(deadline)
			}
			if err != nil || result.Outcome != ConversationCancelled || result.Failure != test.reason || result.Text != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if replayed, err := p.ReplayConversation(0); err != nil || len(replayed) != 0 {
				t.Fatalf("cancelled partial text replayed: %v, %v", replayed, err)
			}
			if cmd.ProcessState == nil {
				t.Fatal("owned child not reaped")
			}
		})
	}
}

func TestGrokConversationBlockedStdinStillReapsOnRevocation(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrokConversationHelperProcess$")
	cmd.Env = append(os.Environ(), "PAIMOS_GROK_STDIN_STALL=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	p := &grokConversationProcess{cmd: cmd, wire: newGrokACPWire(stdout, stdin), stdin: stdin, sessionID: "owned-session", turnID: "turn-1", done: make(chan struct{})}
	go p.run(context.Background(), strings.Repeat("x", 192<<10), GrokConversationOptions{MaxOutputBytes: 32, MaxEvents: 8})
	time.Sleep(50 * time.Millisecond)
	select {
	case <-p.done:
		t.Fatal("blocked child unexpectedly completed")
	default:
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("revocation waited for blocked stdin")
	}
	result, err := p.WaitConversation(ctx)
	if err != nil || result.Outcome != ConversationCancelled || result.Text != "" || cmd.ProcessState == nil {
		t.Fatalf("result=%+v err=%v reaped=%v", result, err, cmd.ProcessState != nil)
	}
}

func TestGrokConversationHelperProcess(t *testing.T) {
	if os.Getenv("PAIMOS_GROK_STDIN_STALL") == "1" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if os.Getenv("PAIMOS_GROK_TEST_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadBytes('\n')
	_, _ = os.Stdout.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"owned-session\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"partial\"}}}}\n")
	_, _ = reader.ReadBytes('\n')
	_, _ = os.Stdout.WriteString("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"stopReason\":\"cancelled\"}}\n")
	os.Exit(0)
}
