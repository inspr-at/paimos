// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var ErrDeclarative = errors.New("declarative installation detected: update the owning Nix/Home Manager configuration through its review path; generated files were preserved")
var ErrServiceConflict = errors.New("existing daemon or service is not owned by this pairing; no service was adopted or changed")

const serviceLabel = "cm.aeon.agentd"

type ServiceReceipt struct {
	Name                string `json:"name"`
	Path                string `json:"path"`
	Digest              string `json:"digest"`
	ComputerID          string `json:"computer_id"`
	ActivationAttempted bool   `json:"activation_attempted"`
	Activated           bool   `json:"activated"`
	Unloaded            bool   `json:"unloaded"`
}

type ServiceManager struct {
	Instance string
	// FixtureLabel is available to isolated platform fixtures, never CLI/server input.
	// Production always leaves it empty. Only the reserved fixture namespace is accepted.
	FixtureLabel string
	Platform     Platform
	Home         string
	UID          int
	Executable   string
	Executor     Executor
	// Systemctl is a locally resolved physical executable, never server input.
	Systemctl string
}

func (m ServiceManager) label() (string, error) {
	if m.FixtureLabel == "" {
		return InstanceLabel(m.Instance)
	}
	if !regexp.MustCompile(`^cm\.aeon\.fixture\.[a-z0-9][a-z0-9-]{7,63}$`).MatchString(m.FixtureLabel) {
		return "", ErrServiceConflict
	}
	return m.FixtureLabel, nil
}

// Definition renders the exact production unit, with optional fixture identity.
func (m ServiceManager) Definition(root string) ([]byte, error) { return m.definition(root) }
func (m ServiceManager) location() (string, string, error) {
	label, err := m.label()
	if err != nil {
		return "", "", err
	}
	if _, err := SupportedPlatform(m.Platform.OS, m.Platform.Arch); err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(m.Home) {
		return "", "", ErrUnsafePath
	}
	if m.Platform.OS == "darwin" {
		return filepath.Join(m.Home, "Library", "LaunchAgents"), label + ".plist", nil
	}
	return filepath.Join(m.Home, ".config", "systemd", "user"), label + ".service", nil
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func unitQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", ErrUnsafePath
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`).Replace(s) + `"`, nil
}

func (m ServiceManager) definition(root string) ([]byte, error) { return m.definitionLogs(root, true) }

func (m ServiceManager) definitionLogs(root string, logs bool) ([]byte, error) {
	if !filepath.IsAbs(m.Executable) || !filepath.IsAbs(root) || strings.ContainsAny(m.Executable+root, "\x00\r\n") {
		return nil, ErrUnsafePath
	}
	label, err := m.label()
	if err != nil {
		return nil, err
	}
	args := []string{m.Executable, "serve", "--setup-root", root}
	if m.Platform.OS == "darwin" {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + label + `</string><key>ProgramArguments</key><array>`)
		for _, a := range args {
			b.WriteString("<string>" + xmlText(a) + "</string>")
		}
		b.WriteString("</array>")
		if logs {
			logDir := filepath.Join(m.Home, "Library", "Logs", "aeon-agentd")
			if m.Instance != "" {
				logDir = filepath.Join(logDir, m.Instance)
			}
			b.WriteString("<key>StandardOutPath</key><string>" + xmlText(filepath.Join(logDir, "stdout.log")) + "</string><key>StandardErrorPath</key><string>" + xmlText(filepath.Join(logDir, "stderr.log")) + "</string>")
		}
		// Successful intentional shutdown is not an invitation to restart.
		b.WriteString(`<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>Umask</key><integer>63</integer></dict></plist>`)
		return []byte(b.String()), nil
	}
	quoted := []string{}
	for _, a := range args {
		q, e := unitQuote(a)
		if e != nil {
			return nil, e
		}
		quoted = append(quoted, q)
	}
	// KillMode=none keeps systemd from signaling model process groups. Helper
	// removal is gated by observed idle, and SIGTERM itself only begins drain.
	return []byte("[Unit]\nDescription=Aeon paired computer\n[Service]\nType=simple\nExecStart=" + strings.Join(quoted, " ") + "\nRestart=on-failure\nUMask=0077\nKillMode=none\nTimeoutStopSec=infinity\n[Install]\nWantedBy=default.target\n"), nil
}

func (m ServiceManager) runner() Executor {
	if m.Executor != nil {
		return m.Executor
	}
	return OSExecutor{}
}
func (m ServiceManager) control(args ...string) Command {
	if m.Platform.OS == "darwin" {
		return Command{Path: "/bin/launchctl", Args: args, DiscardOutput: true}
	}
	return Command{Path: m.Systemctl, Args: append([]string{"--user"}, args...), DiscardOutput: true}
}

// Preflight never adopts a PID, process name or unrecorded loaded service.
func (m ServiceManager) Preflight(ctx context.Context, root string, receipt *ServiceReceipt) error {
	dir, name, err := m.location()
	if err != nil {
		return err
	}
	if m.managedExecutable() || ManagedPath(dir) || ManagedPath(filepath.Join(dir, name)) {
		return ErrDeclarative
	}
	if m.Platform.OS == "linux" && !filepath.IsAbs(m.Systemctl) {
		return errors.New("systemd user manager unavailable")
	}
	if info, e := os.Lstat(filepath.Join(dir, name)); e == nil {
		if receipt == nil || !info.Mode().IsRegular() {
			return ErrServiceConflict
		}
		d, e := openDirectory(dir, false, false)
		if e != nil {
			return e
		}
		defer d.Close()
		raw, e := d.Read(name, 64<<10)
		if e != nil || receipt.Path != filepath.Join(dir, name) || Hash(raw) != receipt.Digest {
			return ErrServiceConflict
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return ErrServiceConflict
	}
	if receipt == nil {
		// Executable identity is discovery evidence only, never a kill target.
		// Exclude this helper; unrelated/classic executables may coexist.
		raw, e := m.runner().Run(ctx, Command{Path: "/bin/ps", Args: []string{"-axo", "pid=,comm="}, OutputLimit: 256 << 10})
		if e != nil {
			return errors.New("daemon ownership discovery unavailable")
		}
		physical, _ := filepath.EvalSymlinks(m.Executable)
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			pid, parseErr := strconv.Atoi(fields[0])
			executable := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
			if m.Instance == "" && parseErr == nil && pid != os.Getpid() && (executable == m.Executable || executable == physical) {
				return ErrServiceConflict
			}
		}
		loaded, e := m.loaded(ctx)
		if e != nil {
			return e
		}
		if loaded {
			return ErrServiceConflict
		}
		if m.Platform.OS == "darwin" && m.FixtureLabel == "" {
			_, e := m.runner().Run(ctx, m.control("print", fmt.Sprintf("gui/%d/at.inspr.aeon-agentd", m.UID)))
			if e == nil {
				return ErrDeclarative
			}
			if !m.absent(e) {
				return errors.New("managed service discovery unavailable")
			}
		}
	}
	_, err = m.definition(root)
	return err
}

func (m ServiceManager) absent(err error) bool {
	var exit *CommandError
	if !errors.As(err, &exit) {
		return false
	}
	if m.Platform.OS == "darwin" {
		return exit.ExitCode == 113
	}
	return exit.ExitCode == 3 || exit.ExitCode == 4
}
func (m ServiceManager) loaded(ctx context.Context) (bool, error) {
	_, name, err := m.location()
	if err != nil {
		return false, err
	}
	label, _ := m.label()
	var c Command
	if m.Platform.OS == "darwin" {
		c = m.control("print", fmt.Sprintf("gui/%d/%s", m.UID, label))
	} else {
		c = m.control("is-active", name)
	}
	_, err = m.runner().Run(ctx, c)
	if err == nil {
		return true, nil
	}
	if m.absent(err) {
		return false, nil
	}
	return false, errors.New("user service ownership is unconfirmed")
}
func savedReceipt(s *Store) (*ServiceReceipt, error) {
	raw, err := s.Read("service.json", 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r ServiceReceipt
	if json.Unmarshal(raw, &r) != nil {
		return nil, ErrServiceConflict
	}
	return &r, nil
}
func writeReceipt(s *Store, r *ServiceReceipt) error {
	b, _ := json.Marshal(r)
	return s.Write("service.json", b, false)
}

// Install is callable only after redeem has returned the exact approved local
// request. The receipt is persisted before activation, covering lost responses.
func (m ServiceManager) Install(ctx context.Context, s *Store, computerID string, approved bool, receipt *ServiceReceipt) (*ServiceReceipt, error) {
	if !approved || computerID == "" {
		return nil, errors.New("authenticated pairing approval required before service activation")
	}
	if saved, err := savedReceipt(s); err != nil {
		return nil, err
	} else if saved != nil {
		receipt = saved
	}
	if receipt != nil && receipt.ComputerID != computerID {
		return nil, ErrServiceConflict
	}
	if err := m.Preflight(ctx, s.Path(), receipt); err != nil {
		return nil, err
	}
	dir, name, _ := m.location()
	d, err := openDirectory(dir, true, false)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	raw, err := m.definition(s.Path())
	if err != nil {
		return nil, err
	}
	if receipt != nil && receipt.Digest != Hash(raw) {
		// Keep exact ownership of older installed units; never silently replace
		// an activated service just to add logging. New installs have log paths.
		legacy, legacyErr := m.definitionLogs(s.Path(), false)
		if legacyErr == nil && receipt.Digest == Hash(legacy) {
			raw = legacy
		}
	}
	if receipt == nil {
		receipt = &ServiceReceipt{Name: name, Path: filepath.Join(dir, name), Digest: Hash(raw), ComputerID: computerID}
	}
	if receipt.Digest != Hash(raw) || receipt.Name != name || receipt.Path != filepath.Join(dir, name) || receipt.Unloaded {
		return nil, ErrServiceConflict
	}
	if m.Platform.OS == "darwin" {
		logs, err := OpenStore(m.logDirectory(), true)
		if err != nil {
			return nil, err
		}
		logs.Close()
	}
	if err = writeReceipt(s, receipt); err != nil {
		return receipt, err
	}
	if err = d.Write(name, raw, true); err != nil && !errors.Is(err, ErrCollision) {
		return nil, err
	}
	actual, err := d.Read(name, 64<<10)
	if err != nil || Hash(actual) != receipt.Digest {
		return nil, ErrServiceConflict
	}
	if receipt.ActivationAttempted {
		loaded, e := m.loaded(ctx)
		if e != nil {
			return receipt, e
		}
		receipt.Activated = loaded
	}
	receipt.ActivationAttempted = true
	if err = writeReceipt(s, receipt); err != nil {
		return receipt, err
	}
	if !receipt.Activated {
		var cmd Command
		if m.Platform.OS == "darwin" {
			cmd = m.control("bootstrap", "gui/"+strconv.Itoa(m.UID), receipt.Path)
		} else {
			if _, err = m.runner().Run(ctx, m.control("daemon-reload")); err != nil {
				return receipt, errors.New("systemd user reload unconfirmed")
			}
			cmd = m.control("enable", "--now", name)
		}
		if _, err = m.runner().Run(ctx, cmd); err != nil {
			return receipt, errors.New("service activation unconfirmed; resume setup to inspect the same owned service")
		}
		receipt.Activated = true
		if err = writeReceipt(s, receipt); err != nil {
			return receipt, err
		}
	}
	return receipt, nil
}

func (m ServiceManager) Remove(ctx context.Context, s *Store, receipt *ServiceReceipt, drained bool) error {
	if !drained {
		return errors.New("service removal blocked until owned process exit is confirmed")
	}
	if receipt == nil {
		return nil
	}
	if saved, err := savedReceipt(s); err != nil {
		return err
	} else if saved != nil {
		if saved.ComputerID != receipt.ComputerID || saved.Path != receipt.Path || saved.Digest != receipt.Digest {
			return ErrServiceConflict
		}
		receipt = saved
	}
	if err := m.Preflight(ctx, s.Path(), receipt); err != nil {
		return err
	}
	dir, name, _ := m.location()
	if receipt.Path != filepath.Join(dir, name) {
		return ErrServiceConflict
	}
	var cmd Command
	if m.Platform.OS == "darwin" {
		cmd = m.control("bootout", "gui/"+strconv.Itoa(m.UID), receipt.Path)
	} else {
		cmd = m.control("disable", "--now", name)
	}
	if !receipt.Unloaded {
		if _, err := m.runner().Run(ctx, cmd); err != nil {
			if m.Platform.OS != "darwin" {
				return errors.New("owned service removal unconfirmed")
			}
			loaded, e := m.loaded(ctx)
			if e != nil || loaded {
				return errors.New("owned service removal unconfirmed")
			}
		}
		receipt.Unloaded = true
		if err := writeReceipt(s, receipt); err != nil {
			return err
		}
	}
	d, err := openDirectory(dir, false, false)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.RemoveExact(name, receipt.Digest); err != nil {
		return err
	}
	if m.Platform.OS == "linux" {
		if _, err = m.runner().Run(ctx, m.control("daemon-reload")); err != nil {
			return errors.New("systemd user reload unconfirmed")
		}
	}
	return nil
}

func (m ServiceManager) logDirectory() string {
	dir := filepath.Join(m.Home, "Library", "Logs", "aeon-agentd")
	if m.Instance != "" {
		dir = filepath.Join(dir, m.Instance)
	}
	return dir
}
