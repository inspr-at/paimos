// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// platform-proof exercises production ownership primitives. It does not install
// credentials, connect to a Paimos server, or infer release acceptance.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/runtimehealth"
)

type receipt struct {
	Schema   int            `json:"schema_version"`
	Status   string         `json:"status"`
	Mode     string         `json:"mode"`
	Platform string         `json:"platform"`
	Started  time.Time      `json:"started_at"`
	Finished time.Time      `json:"finished_at"`
	Stage    string         `json:"stage"`
	Evidence map[string]any `json:"evidence"`
	Cleanup  bool           `json:"cleanup_complete"`
}

func main() {
	if len(os.Args) < 2 {
		fail("expected service or codex")
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	output := flags.String("receipt", "", "new private receipt path")
	root := flags.String("root", "", "new task state root")
	instance := flags.String("instance", "", "unique task instance")
	unit := flags.String("unit", "", "reviewed Linux unit")
	unitHash := flags.String("unit-sha256", "", "reviewed unit digest")
	binary := flags.String("binary", "", "reviewed daemon or Codex executable")
	binaryHash := flags.String("binary-sha256", "", "reviewed executable digest")
	approved := flags.Bool("execute-reviewed-plan", false, "explicit execution gate")
	_ = flags.Parse(os.Args[2:])
	if !*approved || !filepath.IsAbs(*output) || !filepath.IsAbs(*root) || !strings.HasPrefix(filepath.Base(*root), "pai928-") {
		fail("reviewed execution and unique absolute paths required")
	}
	if _, err := os.Lstat(*output); !errors.Is(err, os.ErrNotExist) {
		fail("receipt already exists or unavailable")
	}
	if hashFile(*binary) != *binaryHash || len(*binaryHash) != 64 {
		fail("binary pin mismatch")
	}
	r := receipt{Schema: 1, Status: "FAIL", Mode: os.Args[1], Platform: runtime.GOOS + "/" + runtime.GOARCH, Started: time.Now().UTC(), Evidence: map[string]any{"binary_sha256": *binaryHash, "instance": *instance, "state_root": *root}}
	var err error
	switch os.Args[1] {
	case "service":
		err = serviceProof(&r, *root, *instance, *unit, *unitHash)
	case "codex":
		err = codexProof(&r, *root, *binary)
	default:
		fail("unsupported mode")
	}
	if err == nil && r.Cleanup {
		r.Status = "PASS"
	}
	r.Finished = time.Now().UTC()
	f, writeErr := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if writeErr != nil {
		fail("receipt creation failed")
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	writeErr = enc.Encode(r)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		fail("receipt write failed")
	}
	fmt.Printf("%s %s stage=%s cleanup=%t\n", r.Mode, r.Status, r.Stage, r.Cleanup)
	if r.Status != "PASS" {
		os.Exit(1)
	}
}

func fail(s string) { fmt.Fprintln(os.Stderr, s); os.Exit(2) }
func hashFile(path string) string {
	raw, e := os.ReadFile(path)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func manager(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Fixed verbs and reviewed task unit only; no raw manager diagnostics.
	return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).Run()
}

func serviceProof(r *receipt, root, instance, unit, digest string) (ret error) {
	r.Stage = "service_preflight"
	if runtime.GOOS != "linux" || hashFile(unit) != digest || len(digest) != 64 || filepath.Base(unit) != "paimos-agentd-"+instance+".service" {
		return errors.New("unit pin mismatch")
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return errors.New("state root must be new")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	svc, err := runtimehealth.NewPlatformService("linux", home, instance, root, unit, filepath.Base(unit))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	before, err := svc.Inspect(ctx)
	if err != nil || before.Loaded || before.Running {
		return errors.New("unit collision or unsafe definition")
	}
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	r.Evidence["unit_sha256"] = digest
	r.Evidence["before"] = before
	r.Stage = "link_runtime_unit"
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		stopErr := svc.Stop(stopCtx)
		state, stateErr := svc.Inspect(stopCtx)
		disableErr := manager("disable", "--runtime", filepath.Base(unit))
		reloadErr := manager("daemon-reload")
		after, afterErr := svc.Inspect(stopCtx)
		r.Evidence["after_stop"] = state
		r.Evidence["after_unlink"] = after
		r.Cleanup = stopErr == nil && stateErr == nil && !state.Running && disableErr == nil && reloadErr == nil && afterErr == nil && !after.Loaded && !after.Running
		if !r.Cleanup && ret == nil {
			ret = errors.New("cleanup incomplete")
		}
	}()
	if err = manager("link", "--runtime", unit); err != nil {
		return err
	}
	if err = manager("daemon-reload"); err != nil {
		return err
	}
	dir, err := agentd.InstanceStateDir(root, instance)
	if err != nil {
		return err
	}
	client, err := agentd.NewClient(filepath.Join(dir, "agentd.sock"))
	if err != nil {
		return err
	}
	var first agentd.RuntimeStatus
	for cycle := 1; cycle <= 2; cycle++ {
		r.Stage = fmt.Sprintf("service_start_%d", cycle)
		if err = svc.Start(ctx); err != nil {
			return err
		}
		var status agentd.RuntimeStatus
		deadline := time.Now().Add(8 * time.Second)
		for {
			status, err = client.RuntimeStatus(ctx)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("daemon not ready")
			}
			time.Sleep(50 * time.Millisecond)
		}
		state, inspectErr := svc.Inspect(ctx)
		if inspectErr != nil || !state.Verified || !state.Running || state.PID != status.PID || status.Instance != instance || status.DaemonID == "" || status.ReporterConfigured || len(status.Sessions) != 0 || status.Closed {
			return errors.New("daemon ownership or empty inventory mismatch")
		}
		r.Evidence[fmt.Sprintf("runtime_%d", cycle)] = status
		r.Evidence[fmt.Sprintf("service_%d", cycle)] = state
		if cycle == 1 {
			first = status
		} else if first.DaemonID == status.DaemonID || first.PID == status.PID {
			return errors.New("restart did not create fresh generation")
		}
		r.Stage = fmt.Sprintf("service_stop_%d", cycle)
		if err = svc.Stop(ctx); err != nil {
			return err
		}
		state, err = svc.Inspect(ctx)
		if err != nil || state.Running {
			return errors.New("service remains running")
		}
		if _, err = client.RuntimeStatus(ctx); err == nil {
			return errors.New("old socket remains reachable")
		}
	}
	r.Stage = "complete"
	return nil
}
