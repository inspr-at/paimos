// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func authCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with a PAIMOS instance",
	}
	c.AddCommand(authLoginCmd())
	c.AddCommand(authWhoAmICmd())
	c.AddCommand(authLogoutCmd())
	return c
}

func authLoginCmd() *cobra.Command {
	var (
		urlFlag  string
		nameFlag string
		keyFlag  string
	)
	c := &cobra.Command{
		Use:   "login",
		Short: "Interactively configure a PAIMOS instance + API key",
		Long: `Configures a named PAIMOS instance.

The URL and instance name are written to ~/.paimos/config.yaml; the
API key is entered at the hidden interactive prompt and stored in the
OS keyring (Keychain on macOS, Secret Service or KWallet on Linux,
Credential Manager on Windows) — it never appears in process
arguments, shell history, or on disk.

Headless / CI environments don't log in: set PAIMOS_URL and
PAIMOS_API_KEY together as one runtime-only target that bypasses config
and keyring resolution entirely. Neither value is persisted.

--url skips the URL prompt. The first configured instance becomes
default_instance automatically.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// PAI-685: an argv credential lands in process lists and
			// shell history. The flag stays registered (hidden) so old
			// scripts fail with guidance instead of a parse error — the
			// value is never used.
			if keyFlag != "" {
				keyFlag = ""
				return &usageError{msg: "--api-key was removed (credentials must not appear in process arguments):\n" +
					"  workstations: run `paimos auth login` and use the hidden prompt\n" +
					"  headless/CI:  set " + envURL + " and " + envAPIKey + " as one runtime-only target"}
			}
			stdin := bufio.NewReader(os.Stdin)
			if nameFlag == "" {
				nameFlag = "default"
			}
			if urlFlag == "" {
				fmt.Fprintf(stdout, "Instance URL [https://pm.barta.cm]: ")
				line, _ := stdin.ReadString('\n')
				urlFlag = strings.TrimSpace(line)
				if urlFlag == "" {
					urlFlag = "https://pm.barta.cm"
				}
			}
			urlFlag = normalizeInstanceURL(urlFlag)

			if keyFlag == "" {
				fmt.Fprintf(stdout, "API key (input hidden): ")
				key, err := term.ReadPassword(int(syscall.Stdin))
				fmt.Fprintln(stdout) // newline after hidden input
				if err != nil {
					return fmt.Errorf("read api key: %w", err)
				}
				keyFlag = strings.TrimSpace(string(key))
			}
			if keyFlag == "" {
				return &usageError{msg: "api key is required"}
			}

			// Verify the key works before writing config.
			probe := newClient(InstanceConfig{URL: urlFlag, APIKey: keyFlag})
			body, err := probe.do("GET", "/api/auth/me", nil)
			if err != nil {
				return reportError(err)
			}

			// Store the credential in the OS keyring before touching
			// the config file: if the keyring write fails, we want
			// to bail out without leaving a half-configured instance
			// pointing at a key the user can't retrieve.
			if err := keyringSet(nameFlag, keyFlag); err != nil {
				return fmt.Errorf("%w\n  tip: set %s and %s together to bypass config and keyring resolution (CI / headless)", err, envURL, envAPIKey)
			}

			// Load existing config; append this instance.
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Instances == nil {
				cfg.Instances = map[string]InstanceConfig{}
			}
			cfg.Instances[nameFlag] = InstanceConfig{URL: urlFlag}
			if cfg.DefaultInstance == "" {
				cfg.DefaultInstance = nameFlag
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}

			// /api/auth/me returns {user: {...}, access: {...}}.
			var me struct {
				User map[string]any `json:"user"`
			}
			_ = json.Unmarshal(body, &me)
			username, _ := me.User["username"].(string)
			path, _ := configPath()
			if flagJSON {
				out := map[string]any{
					"ok":       true,
					"instance": nameFlag,
					"url":      urlFlag,
					"config":   path,
					"user":     username,
					"keyring":  keyringServiceName,
				}
				return emitJSON(out)
			}
			fmt.Fprintf(stdout, "✓ logged in as %s at %s\n", username, urlFlag)
			fmt.Fprintf(stdout, "  saved to %s as instance %q\n", path, nameFlag)
			fmt.Fprintf(stdout, "  api key stored in OS keyring (service %q, account %q)\n", keyringServiceName, nameFlag)
			if cfg.DefaultInstance == nameFlag {
				fmt.Fprintf(stdout, "  default_instance = %q\n", nameFlag)
			}
			return nil
		},
	}
	c.Flags().StringVar(&urlFlag, "url", "", "instance URL (skips prompt)")
	c.Flags().StringVar(&nameFlag, "name", "", `name for this instance in config (default "default")`)
	// PAI-685: retired credential path — registered only so legacy
	// invocations get an actionable error instead of "unknown flag".
	c.Flags().StringVar(&keyFlag, "api-key", "", "removed — use the hidden prompt (workstations) or PAIMOS_URL plus PAIMOS_API_KEY (headless)")
	_ = c.Flags().MarkHidden("api-key")
	return c
}

func authWhoAmICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity behind the current API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, inst, err := resolveActiveInstance()
			if err != nil {
				return err
			}
			client := newClient(inst)
			body, err := client.do("GET", "/api/auth/me", nil)
			if err != nil {
				return reportError(err)
			}
			var me struct {
				User map[string]any `json:"user"`
			}
			_ = json.Unmarshal(body, &me)
			if flagJSON {
				out := map[string]any{
					"instance":          name,
					"url":               inst.URL,
					"url_source":        inst.URLSource,
					"credential_source": inst.APIKeySource,
					"user":              me.User,
				}
				return emitJSON(out)
			}
			fmt.Fprintf(stdout, "instance: %s (%s)\n", name, inst.URL)
			if inst.URLSource != "" || inst.APIKeySource != "" {
				fmt.Fprintf(stdout, "source:   url=%s credential=%s\n", inst.URLSource, inst.APIKeySource)
			}
			fmt.Fprintf(stdout, "user:     %v (%v)\n", me.User["username"], me.User["role"])
			return nil
		},
	}
}

func authLogoutCmd() *cobra.Command {
	var (
		nameFlag  string
		removeCfg bool
	)
	c := &cobra.Command{
		Use:   "logout",
		Short: "Remove a stored API key from the OS keyring",
		Long: `Deletes the API key for an instance from the OS keyring.

Without --name, the resolved instance (--instance flag, then
default_instance, then the sole configured instance) is used. Pass
--remove-instance to also drop the URL/name entry from
~/.paimos/config.yaml; otherwise the URL is kept so a later
` + "`paimos auth login --name <name>`" + ` only needs to re-enter the key.

Idempotent: missing keyring entries are not an error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			target := nameFlag
			if target == "" {
				picked, _, err := pickInstance(cfg)
				if err != nil {
					return err
				}
				target = picked
			}
			if err := keyringDelete(target); err != nil {
				return err
			}
			path, _ := configPath()
			if removeCfg {
				if _, ok := cfg.Instances[target]; ok {
					delete(cfg.Instances, target)
					if cfg.DefaultInstance == target {
						cfg.DefaultInstance = ""
						// If exactly one instance remains, promote it
						// so commands keep working without --instance.
						if len(cfg.Instances) == 1 {
							for n := range cfg.Instances {
								cfg.DefaultInstance = n
							}
						}
					}
					if err := saveConfig(cfg); err != nil {
						return err
					}
				}
			}
			if flagJSON {
				return emitJSON(map[string]any{
					"ok":             true,
					"instance":       target,
					"keyring":        keyringServiceName,
					"removed_config": removeCfg,
					"config":         path,
				})
			}
			fmt.Fprintf(stdout, "✓ removed keyring entry for %q (service %q)\n", target, keyringServiceName)
			if removeCfg {
				fmt.Fprintf(stdout, "  removed instance %q from %s\n", target, path)
			}
			return nil
		},
	}
	c.Flags().StringVar(&nameFlag, "name", "", "instance name to log out (default: resolved instance)")
	c.Flags().BoolVar(&removeCfg, "remove-instance", false, "also delete the instance entry from config.yaml")
	return c
}

// emitJSON writes a JSON-encoded value to stdout with a trailing newline.
func emitJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(b))
	return nil
}
