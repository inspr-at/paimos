// SPDX-License-Identifier: AGPL-3.0-only

// Command onboarding-platform exercises the real user-service manager with two
// isolated, nonvendor executables. Run it only on disposable CI runners.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

type fixture struct {
	label   string
	name    string
	ready   string
	stop    string
	exited  string
	unit    string
	store   *agentsetup.Store
	receipt *agentsetup.ServiceReceipt
	manager agentsetup.ServiceManager
}

func main() {
	if os.Getenv("AEON_DISPOSABLE_SERVICE_FIXTURE") != "1" || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_TEMP") == "" {
		fmt.Fprintln(os.Stderr, "refusing service actions outside disposable CI fixture")
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	platform, err := agentsetup.CurrentPlatform()
	if err != nil {
		return err
	}
	if platform.OS != "darwin" && platform.OS != "linux" {
		return errors.New("fixture requires launchd or systemd user")
	}
	if err := managerAvailable(platform.OS); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "aeon-service-fixture-")
	if err != nil {
		return err
	}
	home, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		return err
	}
	serviceHome := home
	if platform.OS == "linux" {
		// The already-running systemd user manager reads units from its login
		// home. CI owns that disposable account; labels and payload stay unique.
		serviceHome, err = os.UserHomeDir()
		if err != nil {
			return err
		}
		serviceHome, err = filepath.EvalSymlinks(serviceHome)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var fixtures []*fixture
	defer func() {
		for _, f := range fixtures {
			_ = os.WriteFile(f.stop, []byte("drain\n"), 0600)
			if f.receipt != nil {
				_ = waitFor(ctx, f.exited)
				_ = f.manager.Remove(ctx, f.store, f.receipt, true)
			}
			_ = f.store.Close()
		}
	}()
	primary, err := newFixture(serviceHome, home, platform, "primary")
	if err != nil {
		return err
	}
	fixtures = append(fixtures, primary)
	neighbor, err := newFixture(serviceHome, home, platform, "neighbor")
	if err != nil {
		return err
	}
	fixtures = append(fixtures, neighbor)
	for _, f := range fixtures {
		if _, err := f.manager.Install(ctx, f.store, f.name, false, nil); err == nil {
			return errors.New("fixture service installed without approval")
		}
		if _, err := os.Stat(f.unit); !errors.Is(err, os.ErrNotExist) {
			return errors.New("unapproved fixture created a service unit")
		}
		if _, err := f.manager.Install(ctx, f.store, f.name, true, nil); err != nil {
			return fmt.Errorf("install %s: %w", f.name, err)
		}
		// The saved receipt is the authority for subsequent lifecycle actions.
		raw, err := f.store.Read("service.json", 64<<10)
		if err != nil {
			return err
		}
		var receipt agentsetup.ServiceReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if receipt.ComputerID != f.name || receipt.Name != filepath.Base(f.unit) {
			return errors.New("service receipt does not identify its fixture")
		}
		f.receipt = &receipt
		if err := waitFor(ctx, f.ready); err != nil {
			return fmt.Errorf("start %s: %w", f.name, err)
		}
		if err := f.active(ctx); err != nil {
			return err
		}
	}
	if err := primary.manager.Remove(ctx, primary.store, primary.receipt, false); err == nil {
		return errors.New("active fixture service removed before drain")
	}
	if err := primary.drainAndRemove(ctx); err != nil {
		return err
	}
	if _, err := os.Stat(primary.unit); !errors.Is(err, os.ErrNotExist) {
		return errors.New("primary unit was not removed exactly")
	}
	if err := neighbor.active(ctx); err != nil {
		return fmt.Errorf("other owned fixture was affected: %w", err)
	}
	if err := neighbor.processAlive(); err != nil {
		return fmt.Errorf("other fixture process was affected: %w", err)
	}
	if err := neighbor.drainAndRemove(ctx); err != nil {
		return err
	}
	fmt.Printf("qualified fixture: %s/%s %s install/status/drain/remove; neighbor preserved\n", runtime.GOOS, runtime.GOARCH, platform.Service)
	return nil
}

func managerAvailable(goos string) error {
	if goos == "darwin" {
		return exec.Command("/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())).Run()
	}
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return errors.New("systemd user manager unavailable on this runner")
	}
	if err := exec.Command(path, "--user", "show", "--property=Version", "--value").Run(); err != nil {
		return errors.New("systemd user manager unavailable on this runner")
	}
	return nil
}

func newFixture(serviceHome, scratchHome string, platform agentsetup.Platform, name string) (*fixture, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	label := "cm.aeon.fixture." + hex.EncodeToString(random) + "-" + name
	root := filepath.Join(scratchHome, name, "state")
	store, err := agentsetup.OpenStore(root, true)
	if err != nil {
		return nil, err
	}
	base := filepath.Join(scratchHome, name)
	ready, stop, exited := filepath.Join(base, "ready"), filepath.Join(base, "stop"), filepath.Join(base, "exited")
	executable := filepath.Join(base, "fake-agentd")
	// This program never invokes a vendor, model, server, or pairing endpoint.
	content := "#!/bin/sh\n" +
		"test \"$1\" = serve && test \"$2\" = --setup-root || exit 64\n" +
		"printf '%s\\n' \"$$\" > '" + ready + "'\n" +
		"while test ! -f '" + stop + "'; do sleep 1; done\n" +
		"printf 'drained\\n' > '" + exited + "'\n"
	if err := os.WriteFile(executable, []byte(content), 0700); err != nil {
		store.Close()
		return nil, err
	}
	systemctl := ""
	if platform.OS == "linux" {
		systemctl, err = exec.LookPath("systemctl")
		if err != nil {
			store.Close()
			return nil, err
		}
		systemctl, err = filepath.EvalSymlinks(systemctl)
		if err != nil {
			store.Close()
			return nil, err
		}
	}
	manager := agentsetup.ServiceManager{FixtureLabel: label, Platform: platform, Home: serviceHome, UID: os.Getuid(), Executable: executable, Systemctl: systemctl}
	unitDir := filepath.Join(serviceHome, "Library", "LaunchAgents")
	unitName := label + ".plist"
	if platform.OS == "linux" {
		unitDir = filepath.Join(serviceHome, ".config", "systemd", "user")
		unitName = label + ".service"
	}
	return &fixture{label: label, name: name, ready: ready, stop: stop, exited: exited, unit: filepath.Join(unitDir, unitName), store: store, manager: manager}, nil
}

func (f *fixture) active(ctx context.Context) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.CommandContext(ctx, "/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+f.label)
	} else {
		cmd = exec.CommandContext(ctx, f.manager.Systemctl, "--user", "is-active", "--quiet", f.label+".service")
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("service %s is not active: %w", f.label, err)
	}
	return nil
}

func (f *fixture) drainAndRemove(ctx context.Context) error {
	if err := os.WriteFile(f.stop, []byte("drain\n"), 0600); err != nil {
		return err
	}
	if err := waitFor(ctx, f.exited); err != nil {
		return err
	}
	if err := f.waitForProcessExit(ctx); err != nil {
		return err
	}
	if err := f.manager.Remove(ctx, f.store, f.receipt, true); err != nil {
		return fmt.Errorf("remove %s: %w", f.name, err)
	}
	return nil
}

func (f *fixture) waitForProcessExit(ctx context.Context) error {
	pid, err := f.processID()
	if err != nil {
		return err
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("fixture process exit unconfirmed")
		case <-tick.C:
		}
	}
}

func (f *fixture) processID() (int, error) {
	raw, err := os.ReadFile(f.ready)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return 0, errors.New("invalid fixture process identity")
	}
	return pid, nil
}

func (f *fixture) processAlive() error {
	pid, err := f.processID()
	if err != nil {
		return err
	}
	return syscall.Kill(pid, 0)
}

func waitFor(ctx context.Context, path string) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s", filepath.Base(path))
		case <-tick.C:
		}
	}
}
