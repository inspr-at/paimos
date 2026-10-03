// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReportStorageRecoversInterruptedAtomicWrites(t *testing.T) {
	for _, target := range []string{"report", "manifest"} {
		for _, point := range []string{"write", "sync", "rename"} {
			t.Run(target+"/"+point, func(t *testing.T) {
				store := FileReports{privateReportRoot(t)}
				id := Identity{Tenant: newUUID(), Project: newUUID(), Attempt: newUUID(), Generation: 1}
				failed, err := store.Put(t.Context(), id, []byte(`{"committed":"failed"}`))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.RetainFailed(t.Context(), failed); err != nil {
					t.Fatal(err)
				}
				id.Attempt, id.Generation = newUUID(), 2
				current, err := store.Put(t.Context(), id, []byte(`{"committed":"current"}`))
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(store.Root, id.Tenant, id.Project)
				pending := id
				pending.Attempt, pending.Generation = newUUID(), 3
				name := "3_" + pending.Attempt + ".report"
				path := filepath.Join(dir, name)
				raw := []byte(`{"uncommitted":"payload"}`)
				if target == "manifest" {
					if err = writePrivate(path, raw); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(dir, "manifest.json")
					raw, err = json.Marshal(reportManifest{Generation: 3, Current: name, Failed: filepath.Base(failed)})
					if err != nil {
						t.Fatal(err)
					}
				}
				// Reproduce actual on-disk crash points. Partial bytes intentionally
				// cannot be parsed; only a renamed manifest commits a generation.
				file, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				payload := raw
				if point == "write" {
					payload = raw[:len(raw)/2]
				}
				if _, err = file.Write(payload); err == nil && point != "write" {
					err = file.Sync()
				}
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("crash fixture: %v, %v", err, closeErr)
				}
				if point == "rename" {
					if err = os.Rename(path+".tmp", path); err != nil {
						t.Fatal(err)
					}
				}
				// Read committed payloads before retry. A renamed manifest is the
				// new committed current; every other point retains generation 2.
				expectedCurrent, expectedRaw := current, `{"committed":"current"}`
				if target == "manifest" && point == "rename" {
					expectedCurrent = id.Tenant + "/" + id.Project + "/" + name
					expectedRaw = `{"uncommitted":"payload"}`
				}
				if got, err := store.Get(t.Context(), expectedCurrent); err != nil || string(got) != expectedRaw {
					t.Fatalf("crash corrupted committed current: %q, %v", got, err)
				}
				// RetainFailed also recovers interrupted manifest writes. Preserve
				// the existing failed reference and then retry the interrupted Put.
				if _, err = store.RetainFailed(t.Context(), failed); err != nil {
					t.Fatalf("interrupted atomic write permanently blocked retention: %v", err)
				}
				if _, err = store.Put(t.Context(), pending, []byte(`{"committed":"retry"}`)); err != nil {
					t.Fatalf("interrupted atomic write permanently blocked retry: %v", err)
				}
				if got, err := store.Get(t.Context(), failed); err != nil || string(got) != `{"committed":"failed"}` {
					t.Fatalf("retry lost committed failed report: %q, %v", got, err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				payloads := 0
				for _, entry := range entries {
					if strings.HasSuffix(entry.Name(), ".tmp") {
						t.Fatal("owned temporary report was not recovered")
					}
					if reportName.MatchString(entry.Name()) {
						payloads++
					}
				}
				if payloads != 2 {
					t.Fatalf("retry broke committed report retention: %d", payloads)
				}
			})
		}
	}
}

func TestReportRecoveryPreservesUnexpectedAndUnsafeItems(t *testing.T) {
	for _, which := range []string{"unexpected", "symlink", "public-temp"} {
		t.Run(which, func(t *testing.T) {
			store := FileReports{privateReportRoot(t)}
			id := Identity{Tenant: newUUID(), Project: newUUID(), Attempt: newUUID(), Generation: 1}
			ref, err := store.Put(t.Context(), id, []byte(`{"committed":true}`))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(store.Root, id.Tenant, id.Project)
			path := filepath.Join(dir, "manifest.json.tmp")
			switch which {
			case "unexpected":
				path = filepath.Join(dir, "unexpected.item")
				err = os.WriteFile(path, []byte("preserve"), 0600)
			case "symlink":
				err = os.Symlink(filepath.Join(dir, filepath.Base(ref)), path)
			case "public-temp":
				err = os.WriteFile(path, []byte("preserve"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			id.Attempt, id.Generation = newUUID(), 2
			if _, err = store.Put(t.Context(), id, []byte(`{"replacement":true}`)); err == nil {
				t.Fatal("unsafe or unexpected item allowed maintenance")
			}
			if _, err = os.Lstat(path); err != nil {
				t.Fatal("unsafe or unexpected item was removed", err)
			}
			if got, err := store.Get(t.Context(), ref); err != nil || string(got) != `{"committed":true}` {
				t.Fatal("unsafe item corrupted the committed report", err)
			}
		})
	}
}

func TestCommandProviderBoundsInheritedDescriptorsAndDescendants(t *testing.T) {
	for _, scenario := range []string{"cancel", "leader-exit"} {
		t.Run(scenario, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err = listener.(*net.TCPListener).SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(t.TempDir(), "provider")
			quoted := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "'"
			if err = os.WriteFile(script, []byte("#!/bin/sh\nexec "+quoted+" -test.run='^TestCommandProviderProcessHelper$' aeon-adoption-v1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AEON_ADOPTION_HELPER_ROLE", "parent")
			t.Setenv("AEON_ADOPTION_HELPER_SCENARIO", scenario)
			t.Setenv("AEON_ADOPTION_HELPER_ADDRESS", listener.Addr().String())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := (CommandProvider{script}).Capabilities(ctx)
				done <- err
			}()
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal("child never established the descriptor barrier", err)
			}
			defer conn.Close()
			if err = conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			group, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || group <= 0 {
				t.Fatal("invalid owned helper group")
			}
			defer syscall.Kill(-group, syscall.SIGKILL)
			// Child is alive, holds both output descriptors and blocks on our
			// connection. Cancellation happens only after this explicit barrier.
			if scenario == "cancel" {
				cancel()
			}
			select {
			case err = <-done:
				if err == nil || err.Error() != "backup provider command failed" {
					t.Fatalf("inherited descriptors reported success or wrong failure: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("provider hung after cancellation or leader exit")
			}
			var one [1]byte
			if _, err = conn.Read(one[:]); !errors.Is(err, io.EOF) {
				t.Fatalf("provider descendant survived the bounded call: %v", err)
			}
		})
	}
}

func TestCommandProviderProcessHelper(t *testing.T) {
	role := os.Getenv("AEON_ADOPTION_HELPER_ROLE")
	if role == "" {
		return
	}
	if role == "parent" {
		binary, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		child := exec.Command(binary, "-test.run=^TestCommandProviderProcessHelper$", "aeon-adoption-v1")
		child.Env = append(os.Environ(), "AEON_ADOPTION_HELPER_ROLE=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(2)
		}
		if os.Getenv("AEON_ADOPTION_HELPER_SCENARIO") == "leader-exit" {
			os.Exit(0)
		}
		_ = child.Wait()
		os.Exit(2)
	}
	conn, err := net.Dial("tcp", os.Getenv("AEON_ADOPTION_HELPER_ADDRESS"))
	if err != nil {
		os.Exit(2)
	}
	group, err := syscall.Getpgid(0)
	if err != nil {
		os.Exit(2)
	}
	if _, err = fmt.Fprintf(conn, "%d\n", group); err != nil {
		os.Exit(2)
	}
	var one [1]byte
	_, _ = conn.Read(one[:])
	os.Exit(2)
}
