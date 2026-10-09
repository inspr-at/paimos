// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var instancePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func ValidateInstance(name string) error {
	if name != "" && !instancePattern.MatchString(name) {
		return errors.New("instance must be a lowercase name of at most 32 characters")
	}
	return nil
}
func InstanceLabel(name string) (string, error) {
	if err := ValidateInstance(name); err != nil {
		return "", err
	}
	if name == "" {
		return serviceLabel, nil
	}
	return serviceLabel + "." + name, nil
}
func validInstanceLabel(label string) bool {
	if label == serviceLabel {
		return true
	}
	return strings.HasPrefix(label, serviceLabel+".") && ValidateInstance(strings.TrimPrefix(label, serviceLabel+".")) == nil
}
func InstanceStateRoot(goos, home, xdg, name string) (string, error) {
	if err := ValidateInstance(name); err != nil {
		return "", err
	}
	root, err := DefaultStateRoot(goos, home, xdg)
	if err != nil {
		return "", err
	}
	if name != "" {
		root += "-" + name
	}
	return root, nil
}
func DefaultLedgerRoot(goos, home, xdg string) (string, error) {
	root, err := DefaultStateRoot(goos, home, xdg)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), "ledger"), nil
}

type InstalledLedgerService struct{ Label, Root, Executable string }

// InstalledLedgerServices checks every installed definition, including stopped
// services. It neither unloads services nor writes plists or receipts. Nix-owned
// definitions remain the configuration owner's responsibility.
func (m ServiceManager) InstalledLedgerServices(ctx context.Context) ([]InstalledLedgerService, error) {
	if m.Platform.OS != "darwin" {
		return nil, ErrDeclarative
	}
	dir := filepath.Join(m.Home, "Library", "LaunchAgents")
	store, err := openDirectory(dir, false, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer store.Close()
	names, err := store.Names(1024)
	if err != nil {
		return nil, err
	}
	out := []InstalledLedgerService{}
	for _, name := range names {
		if name == "at.inspr.aeon-agentd.plist" {
			return nil, ErrDeclarative
		}
		if !strings.HasPrefix(name, serviceLabel) || !strings.HasSuffix(name, ".plist") {
			continue
		}
		if ManagedPath(filepath.Join(dir, name)) {
			return nil, ErrDeclarative
		}
		raw, err := store.Read(name, 64<<10)
		if err != nil {
			return nil, ErrServiceConflict
		}
		service, err := parseLedgerPlist(raw)
		if err != nil || name != service.Label+".plist" {
			return nil, ErrServiceConflict
		}
		if err = m.checkLedgerExecutable(ctx, service.Executable); err != nil {
			return nil, err
		}
		// Check a loaded definition too: an updated plist does not replace launchd's
		// retained program. Unknown loaded programs cannot authorize a transition.
		loaded := m
		loaded.Instance = strings.TrimPrefix(service.Label, serviceLabel+".")
		if service.Label == serviceLabel {
			loaded.Instance = ""
		}
		command := loaded.control("print", "gui/"+itoa(m.UID)+"/"+service.Label)
		command.DiscardOutput = false
		command.OutputLimit = 64 << 10
		op, cancel := context.WithTimeout(ctx, 3*time.Second)
		loadedRaw, e := m.runner().Run(op, command)
		cancel()
		if e == nil {
			program := ""
			for _, line := range strings.Split(string(loadedRaw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "program = ") {
					program = strings.TrimSpace(strings.TrimPrefix(line, "program = "))
				}
			}
			if program == "" || program != service.Executable {
				return nil, ErrServiceConflict
			}
			if err = m.checkLedgerExecutable(ctx, program); err != nil {
				return nil, err
			}
		} else if !m.absent(e) {
			return nil, ErrServiceConflict
		}
		out = append(out, service)
	}
	return out, nil
}
func itoa(n int) string { return strconv.Itoa(n) }
func (m ServiceManager) checkLedgerExecutable(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") || ManagedPath(path) {
		return ErrDeclarative
	}
	op, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := m.runner().Run(op, Command{Path: path, Args: []string{"--version"}, OutputLimit: 4096})
	if err != nil || !strings.Contains(string(raw), "ledger-v1") {
		return errors.New("installed service executable does not advertise ledger-v1")
	}
	return nil
}
func parseLedgerPlist(raw []byte) (InstalledLedgerService, error) {
	var out InstalledLedgerService
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	key := ""
	args := []string{}
	inArgs := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, ErrServiceConflict
		}
		switch token := token.(type) {
		case xml.StartElement:
			switch token.Name.Local {
			case "key":
				if decoder.DecodeElement(&key, &token) != nil {
					return out, ErrServiceConflict
				}
			case "array":
				inArgs = key == "ProgramArguments"
			case "string":
				var value string
				if decoder.DecodeElement(&value, &token) != nil {
					return out, ErrServiceConflict
				}
				if inArgs {
					args = append(args, value)
				} else if key == "Label" {
					out.Label = value
				}
			}
		case xml.EndElement:
			if token.Name.Local == "array" {
				inArgs = false
			}
		}
	}
	if !validInstanceLabel(out.Label) || len(args) != 4 || args[1] != "serve" || args[2] != "--setup-root" || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[3]) {
		return out, ErrServiceConflict
	}
	out.Executable, out.Root = args[0], args[3]
	return out, nil
}

func RecordedServiceLabel(root string) (string, error) {
	store, err := OpenStoreReadOnly(root)
	if err != nil {
		return "", err
	}
	defer store.Close()
	receipt, err := savedReceipt(store)
	if err != nil {
		return "", err
	}
	if receipt == nil {
		return serviceLabel, nil
	}
	label := strings.TrimSuffix(strings.TrimSuffix(receipt.Name, ".plist"), ".service")
	if !validInstanceLabel(label) {
		return "", ErrServiceConflict
	}
	return label, nil
}
