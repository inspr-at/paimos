// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

// RunCommand receives only fixed platform-manager argv. Output is bounded and
// consumed internally; raw diagnostics must never reach the public report.
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type RunCommand func(context.Context, string, ...string) ([]byte, error)
type PlatformService struct {
	mu                                              sync.Mutex
	ExpectedURL                                     string
	Platform, Home, Instance, StateRoot, File, Name string
	Run                                             RunCommand
	fingerprint                                     string
}

func NewPlatformService(platform, home, instance, root, file, name string) (*PlatformService, error) {
	if platform != "darwin" && platform != "linux" {
		return nil, errors.New("runtime supports macOS LaunchAgents and Linux user services")
	}
	if !namePattern.MatchString(instance) || !filepath.IsAbs(home) || !filepath.IsAbs(root) {
		return nil, errors.New("service configuration invalid")
	}
	if name == "" {
		if platform == "darwin" {
			name = "cm.paimos.agentd." + instance
		} else {
			name = "paimos-agentd-" + instance + ".service"
		}
	}
	if !serviceNamePattern.MatchString(name) {
		return nil, errors.New("service name invalid")
	}
	if file == "" {
		switch platform {
		case "darwin":
			file = filepath.Join(home, "Library", "LaunchAgents", name+".plist")
		case "linux":
			file = filepath.Join(home, ".config", "systemd", "user", name)
		default:
			return nil, errors.New("runtime supports macOS LaunchAgents and Linux user services")
		}
	}
	if !filepath.IsAbs(file) || strings.ContainsAny(file, "\x00\r\n") {
		return nil, errors.New("service file invalid")
	}
	return &PlatformService{Platform: platform, Home: home, Instance: instance, StateRoot: root, File: file, Name: name, Run: runManager}, nil
}
func runManager(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, errors.New("platform manager unavailable")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err = cmd.Run()
	if output.overflow {
		return nil, errors.New("platform response exceeds bound")
	}
	return output.Bytes(), err
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (256 << 10) - b.Len()
	if n > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (p *PlatformService) InstallAction() string {
	if p.Platform == "darwin" {
		return "launchctl bootstrap gui/" + strconv.Itoa(os.Getuid()) + " " + shellQuote(p.File)
	}
	return "systemctl --user enable --now " + shellQuote(p.Name)
}
func (p *PlatformService) definition() (string, error) {
	// Following a declaration symlink is read-only and supports Home Manager.
	// The resolved file must be immutable-to-others and owned by root/operator.
	actual, err := filepath.EvalSymlinks(p.File)
	if err != nil {
		return "", errors.New("service definition missing")
	}
	f, err := os.Open(actual)
	if err != nil {
		return "", errors.New("service definition unavailable")
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 || !trustedDefinitionOwner(i) || i.Size() > 64<<10 {
		return "", errors.New("service definition unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", errors.New("service definition unavailable")
	}
	var args []string
	if p.Platform == "darwin" {
		args, err = launchArguments(raw, p.Name, p.StateRoot, p.Instance)
	} else {
		args, err = unitArguments(raw)
	}
	if err != nil || verifyServeArgs(args, p.Instance, p.StateRoot, p.ExpectedURL) != nil {
		return "", errors.New("service definition does not prove exact instance ownership")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
func (p *PlatformService) Inspect(ctx context.Context) (ServiceState, error) {
	fp, err := p.definition()
	if err != nil {
		return ServiceState{}, err
	}
	p.mu.Lock()
	changed := p.fingerprint != "" && p.fingerprint != fp
	if !changed {
		p.fingerprint = fp
	}
	p.mu.Unlock()
	if changed {
		return ServiceState{}, errors.New("service definition changed during operation")
	}
	s := ServiceState{Verified: true, Definition: fp}
	if p.Platform == "darwin" {
		// launchctl list without a label is a PID/status/name table. Unlike print,
		// it never dumps resolved environment, arguments or reporter key paths.
		raw, e := p.Run(ctx, "launchctl", "list")
		if e != nil {
			return s, errors.New("LaunchAgent state unavailable")
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) == 3 && f[2] == p.Name {
				s.Loaded = true
				s.PID, _ = strconv.Atoi(f[0])
				s.Running = s.PID > 0
				exit, _ := strconv.Atoi(f[1])
				if exit != 0 {
					s.Restarts = 1
				}
			}
		}
	} else {
		raw, e := p.Run(ctx, "systemctl", "--user", "show", p.Name, "--property=LoadState,ActiveState,MainPID,NRestarts,FragmentPath,DropInPaths", "--no-pager")
		if e != nil {
			return s, errors.New("user service state unavailable")
		}
		values := map[string]string{}
		for _, line := range strings.Split(string(raw), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				values[k] = v
			}
		}
		if values["DropInPaths"] != "" {
			return ServiceState{}, errors.New("service overrides require operator review")
		}
		fragment, e := filepath.EvalSymlinks(values["FragmentPath"])
		expected, ee := filepath.EvalSymlinks(p.File)
		if values["LoadState"] == "loaded" && (e != nil || ee != nil || fragment != expected) {
			return ServiceState{}, errors.New("loaded service definition mismatch")
		}
		s.Loaded = values["LoadState"] == "loaded"
		s.PID, _ = strconv.Atoi(values["MainPID"])
		s.Running = values["ActiveState"] == "active" && s.PID > 0
		s.Restarts, _ = strconv.Atoi(values["NRestarts"])
	}
	return s, nil
}
func (p *PlatformService) Start(ctx context.Context) error {
	s, e := p.Inspect(ctx)
	if e != nil || !s.Verified {
		return errors.New("verified service required")
	}
	if s.Running {
		return nil
	}
	if p.Platform == "darwin" {
		if s.Loaded {
			_, e = p.Run(ctx, "launchctl", "kickstart", "gui/"+strconv.Itoa(os.Getuid())+"/"+p.Name)
		} else {
			_, e = p.Run(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), p.File)
		}
	} else {
		_, e = p.Run(ctx, "systemctl", "--user", "start", p.Name)
	}
	if e != nil {
		return errors.New("service start failed")
	}
	return nil
}
func (p *PlatformService) Stop(ctx context.Context) error {
	s, e := p.Inspect(ctx)
	if e != nil || !s.Verified {
		return errors.New("verified service required")
	}
	if !s.Loaded {
		return nil
	}
	if p.Platform == "darwin" {
		_, e = p.Run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+p.Name)
	} else {
		_, e = p.Run(ctx, "systemctl", "--user", "stop", p.Name)
	}
	if e != nil {
		return errors.New("service stop failed")
	}
	return nil
}
func verifyServeArgs(args []string, instance, root, expectedURL string) error {
	if len(args) < 4 || !filepath.IsAbs(args[0]) || filepath.Base(args[0]) != "paimos-agentd" || args[1] != "serve" {
		return errors.New("direct agentd executable required")
	}
	exe, e := os.Stat(args[0])
	if e != nil || !exe.Mode().IsRegular() || exe.Mode().Perm()&0111 == 0 || exe.Mode().Perm()&0022 != 0 || !trustedDefinitionOwner(exe) {
		return errors.New("agentd executable unavailable")
	}
	values := map[string]string{}
	for _, a := range args {
		if strings.ContainsAny(a, "\x00\r\n") {
			return errors.New("service arguments invalid")
		}
	}
	for i := 2; i < len(args); i++ {
		k := args[i]
		switch k {
		case "--allow-shared-workspaces":
			return errors.New("shared workspace service requires separate review")
		case "--instance", "--state-root", "--socket", "--codex-path", "--claude-path", "--node-path", "--claude-sdk-path", "--report-host", "--report-url", "--report-api-key-file", "--paimos-path", "--lifecycle-config":
		default:
			return errors.New("service arguments unsupported")
		}
		if i+1 >= len(args) || values[k] != "" {
			return errors.New("service arguments ambiguous")
		}
		i++
		values[k] = args[i]
	}
	if values["--instance"] != instance || values["--state-root"] != root {
		return errors.New("service scope mismatch")
	}
	dir, _ := agentd.InstanceStateDir(root, instance)
	if v := values["--socket"]; v != "" && v != filepath.Join(dir, "agentd.sock") {
		return errors.New("service socket outside owned directory")
	}
	for _, key := range []string{"--codex-path", "--claude-path", "--node-path", "--claude-sdk-path", "--paimos-path"} {
		if v := values[key]; v != "" {
			i, e := os.Stat(v)
			if !filepath.IsAbs(v) || e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 || key != "--claude-sdk-path" && i.Mode().Perm()&0111 == 0 {
				return errors.New("service executable path unavailable")
			}
		}
	}
	report := values["--report-host"] != "" || values["--report-url"] != "" || values["--report-api-key-file"] != ""
	if v := values["--lifecycle-config"]; v != "" {
		if !report || !filepath.IsAbs(v) {
			return errors.New("lifecycle service configuration invalid")
		}
		if _, e := safeFile(v, false); e != nil {
			return errors.New("lifecycle configuration metadata unsafe")
		}
	}
	if report {
		if expectedURL != "" && strings.TrimRight(values["--report-url"], "/") != strings.TrimRight(expectedURL, "/") {
			return errors.New("reporter deployment mismatch")
		}
		if values["--report-host"] == "" || values["--report-url"] == "" || !filepath.IsAbs(values["--report-api-key-file"]) {
			return errors.New("reporter configuration incomplete")
		}
		if _, e := safeFile(values["--report-api-key-file"], false); e != nil {
			return errors.New("reporter file metadata unsafe")
		}
	}
	return nil
}

// Only a single direct ExecStart is accepted. Shell wrappers, specifier/env
// substitution, extra lifecycle commands and unknown execution hooks fail closed.
func unitArguments(raw []byte) ([]string, error) {
	section := ""
	var args []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("unit syntax unsupported")
		}
		k = strings.TrimSpace(k)
		if section == "[Service]" {
			if k == "ExecStart" {
				if args != nil {
					return nil, errors.New("multiple start commands")
				}
				var e error
				args, e = splitUnitCommand(v)
				if e != nil {
					return nil, e
				}
			} else if k == "StandardOutput" || k == "StandardError" {
				if v != "journal" && v != "null" && v != "inherit" {
					return nil, errors.New("user service log output outside managed journal")
				}
			} else if strings.HasPrefix(k, "Exec") || k == "Environment" || k == "EnvironmentFile" || k == "User" || k == "RootDirectory" || k == "RootImage" {
				return nil, errors.New("service execution override unsupported")
			}
		}
	}
	if args == nil {
		return nil, errors.New("start command missing")
	}
	return args, nil
}
func splitUnitCommand(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	var quote rune
	for _, c := range s {
		if c == '%' || c == '$' || c == '\\' || c == '\r' {
			return nil, errors.New("unit interpolation unsupported")
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				b.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == ' ' || c == '\t' {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		} else {
			b.WriteRune(c)
		}
	}
	if quote != 0 {
		return nil, errors.New("unit quoting invalid")
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out, nil
}
func launchArguments(raw []byte, name, root, instance string) ([]string, error) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	depth := 0
	key := ""
	seen := map[string]bool{}
	label := ""
	logs := map[string]string{}
	umask := -1
	var args []string
	for {
		tok, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, errors.New("LaunchAgent syntax invalid")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "key" && depth == 3 {
				if e = d.DecodeElement(&key, &t); e != nil {
					return nil, e
				}
				depth--
				if seen[key] {
					return nil, errors.New("duplicate LaunchAgent key")
				}
				seen[key] = true
				if key == "EnvironmentVariables" || key == "Program" || key == "UserName" {
					return nil, errors.New("LaunchAgent execution override unsupported")
				}
			} else if key == "Label" && depth == 3 {
				if label != "" {
					return nil, errors.New("duplicate Label")
				}
				if e = d.DecodeElement(&label, &t); e != nil {
					return nil, e
				}
				depth--
			} else if key == "Umask" && depth == 3 {
				if umask != -1 || t.Name.Local != "integer" {
					return nil, errors.New("LaunchAgent umask invalid")
				}
				if e = d.DecodeElement(&umask, &t); e != nil {
					return nil, e
				}
				depth--
			} else if (key == "StandardOutPath" || key == "StandardErrorPath") && depth == 3 {
				if logs[key] != "" || t.Name.Local != "string" {
					return nil, errors.New("LaunchAgent log configuration invalid")
				}
				var value string
				if e = d.DecodeElement(&value, &t); e != nil {
					return nil, e
				}
				logs[key] = value
				depth--
			} else if key == "ProgramArguments" && depth == 3 {
				if args != nil || t.Name.Local != "array" {
					return nil, errors.New("LaunchAgent arguments invalid")
				}
				var a struct {
					Strings []string `xml:"string"`
				}
				if e = d.DecodeElement(&a, &t); e != nil {
					return nil, e
				}
				args = a.Strings
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}
	dir, _ := agentd.InstanceStateDir(root, instance)
	if len(logs) > 0 && umask != 63 {
		return nil, errors.New("LaunchAgent file logs require private umask")
	}
	for _, path := range logs {
		if path != filepath.Join(dir, "agentd.log") && path != filepath.Join(dir, "agentd.stdout.log") && path != filepath.Join(dir, "agentd.stderr.log") {
			return nil, errors.New("LaunchAgent logs outside private runtime directory")
		}
	}
	if label != name || args == nil {
		return nil, fmt.Errorf("LaunchAgent label or arguments mismatch")
	}
	return args, nil
}
