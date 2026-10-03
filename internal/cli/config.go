// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileConfig is ~/.aeon/config.yaml. API keys are stored in a sibling keys
// directory, not in this file.
type fileConfig struct {
	DefaultInstance string                  `yaml:"default_instance"`
	Instances       map[string]fileInstance `yaml:"instances,omitempty"`
}

type fileInstance struct {
	AgentdStateRoot string `yaml:"agentd_state_root,omitempty"`
	URL             string `yaml:"url"`
	APIKey          string `yaml:"api_key,omitempty"`
}

type resolvedInstance struct {
	AgentdStateRoot string
	Name            string
	URL             string
	APIKey          string
}

func (rt *runtime) configFile() (string, error) {
	if strings.TrimSpace(rt.configPath) != "" {
		return rt.configPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	program := ".aeon"
	if rt.program == "paimos" {
		program = ".paimos"
	}
	return filepath.Join(home, program, "config.yaml"), nil
}

func (rt *runtime) loadConfig() (fileConfig, string, error) {
	path, err := rt.configFile()
	if err != nil {
		return fileConfig{}, "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileConfig{}, path, nil
		}
		return fileConfig{}, path, fmt.Errorf("read config: %w", err)
	}
	var cfg fileConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fileConfig{}, path, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Instances == nil {
		cfg.Instances = map[string]fileInstance{}
	}
	moved := false
	for name, inst := range cfg.Instances {
		if strings.TrimSpace(inst.APIKey) == "" {
			continue
		}
		if err := validInstanceName(name); err != nil {
			return fileConfig{}, path, err
		}
		if err := writeKey(keyPath(path, name), strings.TrimSpace(inst.APIKey)); err != nil {
			return fileConfig{}, path, err
		}
		inst.APIKey = ""
		cfg.Instances[name] = inst
		moved = true
	}
	if moved {
		if err := saveConfig(path, cfg); err != nil {
			return fileConfig{}, path, err
		}
		fmt.Fprintf(rt.stderr, "%s: moved API keys out of %s into %s\n", rt.program, path, filepath.Join(filepath.Dir(path), "keys"))
	}
	return cfg, path, nil
}

func saveConfig(path string, cfg fileConfig) error {
	scrubbed := fileConfig{DefaultInstance: cfg.DefaultInstance}
	if len(cfg.Instances) > 0 {
		scrubbed.Instances = make(map[string]fileInstance, len(cfg.Instances))
		for name, inst := range cfg.Instances {
			inst.APIKey = ""
			scrubbed.Instances[name] = inst
		}
	}
	raw, err := yaml.Marshal(&scrubbed)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writePrivate(path, raw)
}

func writePrivate(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	return os.Chmod(path, 0o600)
}

func keyPath(configPath, name string) string {
	return filepath.Join(filepath.Dir(configPath), "keys", name)
}

func writeKey(path, key string) error {
	return writePrivate(path, []byte(key+"\n"))
}

func readKeyFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return oneLineSecret(string(raw))
}

func oneLineSecret(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("API key must be a single non-empty line")
	}
	return s, nil
}

func validInstanceName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 64 {
		return usagef("invalid instance name %q", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return usagef("invalid instance name %q", name)
		}
	}
	return nil
}

func (rt *runtime) resolve() (resolvedInstance, error) {
	if fromEnv, ok, err := rt.resolveEnv(); ok || err != nil {
		return fromEnv, err
	}
	cfg, path, err := rt.loadConfig()
	if err != nil {
		return resolvedInstance{}, err
	}
	name, inst, err := pickInstance(cfg, rt.instance)
	if err != nil {
		return resolvedInstance{}, err
	}
	key, err := rt.storedKey(path, name, inst)
	if err != nil {
		return resolvedInstance{}, err
	}
	return resolvedInstance{Name: name, URL: inst.URL, APIKey: key, AgentdStateRoot: inst.AgentdStateRoot}, nil
}

func pickInstance(cfg fileConfig, flag string) (string, fileInstance, error) {
	if len(cfg.Instances) == 0 {
		return "", fileInstance{}, usagef("no instance configured — run auth login")
	}
	name := strings.TrimSpace(flag)
	if name == "" {
		name = cfg.DefaultInstance
	}
	if name == "" && len(cfg.Instances) == 1 {
		for n := range cfg.Instances {
			name = n
		}
	}
	if name == "" {
		return "", fileInstance{}, usagef("multiple instances configured; pass --instance")
	}
	inst, ok := cfg.Instances[name]
	if !ok {
		return "", fileInstance{}, usagef("instance %q not found", name)
	}
	if strings.TrimSpace(inst.URL) == "" {
		return "", fileInstance{}, usagef("instance %q has no URL", name)
	}
	return name, inst, nil
}

func (rt *runtime) storedKey(configPath, name string, inst fileInstance) (string, error) {
	key, err := readKeyFile(keyPath(configPath, name))
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read API key: %w", err)
	}
	if strings.TrimSpace(inst.APIKey) != "" {
		return strings.TrimSpace(inst.APIKey), nil
	}
	return "", usagef("no API key for instance %q — run %s auth login --name %s", name, rt.program, name)
}

func (rt *runtime) resolveEnv() (resolvedInstance, bool, error) {
	urlVar, keyVar, fileVar, ok := rt.envNames()
	if !ok {
		return resolvedInstance{}, false, nil
	}
	if strings.TrimSpace(rt.instance) != "" {
		return resolvedInstance{}, true, usagef("--instance %q conflicts with %s", rt.instance, urlVar)
	}
	rawURL := strings.TrimSpace(os.Getenv(urlVar))
	key, err := envKey(keyVar, fileVar)
	if err != nil {
		return resolvedInstance{}, true, err
	}
	if key == "" {
		return resolvedInstance{}, true, usagef("%s is set but neither %s nor %s is configured", urlVar, keyVar, fileVar)
	}
	u, err := normalizeURL(rawURL)
	if err != nil {
		return resolvedInstance{}, true, err
	}
	return resolvedInstance{Name: "env", URL: u, APIKey: key}, true, nil
}

func (rt *runtime) envNames() (urlVar, keyVar, fileVar string, ok bool) {
	if strings.TrimSpace(os.Getenv("AEON_URL")) != "" {
		return "AEON_URL", "AEON_API_KEY", "AEON_API_KEY_FILE", true
	}
	if rt.program == "paimos" && strings.TrimSpace(os.Getenv("PAIMOS_URL")) != "" {
		return "PAIMOS_URL", "PAIMOS_API_KEY", "PAIMOS_API_KEY_FILE", true
	}
	return "", "", "", false
}

func envKey(keyVar, fileVar string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(keyVar))
	path := strings.TrimSpace(os.Getenv(fileVar))
	if raw != "" && path != "" {
		return "", usagef("%s and %s are both set; choose one", keyVar, fileVar)
	}
	if path == "" {
		return raw, nil
	}
	if path == "-" {
		return "", usagef("%s cannot read from stdin", fileVar)
	}
	key, err := readKeyFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", fileVar, err)
	}
	return key, nil
}
