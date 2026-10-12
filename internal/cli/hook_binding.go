// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hookbinding"
)

// Inspect literal hook commands, never execute them or expand shell syntax.
// Requiring the heartbeat's CLI avoids old readers silently rejecting the
// private source index. A current generic hook inherits its indexed instance.
func inboxHookCheck(worktree, home, executable, instance string) string {
	paths := []string{filepath.Join(worktree, ".claude", "settings.local.json"), hookSettingsPath(home, "claude")}
	current, err := os.Stat(executable)
	if err != nil {
		return "hook_cli_mismatch"
	}
	found := false
	for _, path := range paths {
		raw, missing, err := readHarnessFile(path)
		if err != nil {
			return "hook_unavailable"
		}
		if missing {
			continue
		}
		var settings struct {
			Hooks map[string][]struct {
				Hooks []struct{ Type, Command string }
			}
		}
		if json.Unmarshal([]byte(raw), &settings) != nil {
			return "hook_unavailable"
		}
		for event, groups := range settings.Hooks {
			if event != "Stop" && event != "PostToolUse" && event != "UserPromptSubmit" {
				continue
			}
			for _, group := range groups {
				for _, hook := range group.Hooks {
					if hook.Type != "command" || !strings.Contains(hook.Command, " hook claude ") {
						continue
					}
					command := strings.TrimSpace(hook.Command)
					for _, marker := range []string{inboxHookMarker, " # aeon-inbox-hook-v2"} {
						command = strings.TrimSuffix(command, marker)
					}
					args, err := staticHookArgs(command)
					if err != nil || len(args) < 4 || !filepath.IsAbs(args[0]) {
						return "hook_unavailable"
					}
					info, err := os.Stat(args[0])
					if err != nil || !os.SameFile(current, info) {
						return "hook_cli_mismatch"
					}
					for i, arg := range args {
						if arg == "--paired" {
							return "hook_unavailable"
						}
						if arg == "--instance" && (i+1 == len(args) || args[i+1] != instance) {
							return "hook_instance_mismatch"
						}
						if strings.HasPrefix(arg, "--instance=") && strings.TrimPrefix(arg, "--instance=") != instance {
							return "hook_instance_mismatch"
						}
					}
					found = true
				}
			}
		}
	}
	if !found {
		return "hook_unavailable"
	}
	return ""
}

func pairedBindingCheck(origin, principal string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "daemon_unavailable"
	}
	root, err := agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
	if err != nil {
		return "daemon_unavailable"
	}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return "daemon_unavailable"
	}
	defer store.Close()
	raw, err := store.Read("runtime.json", 64<<10)
	if err != nil {
		return "daemon_unavailable"
	}
	var metadata struct {
		Origin    string `json:"origin"`
		Principal string `json:"principal_id"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.Principal == "" || metadata.Origin == "" {
		return "daemon_unavailable"
	}
	if strings.TrimRight(metadata.Origin, "/") != strings.TrimRight(origin, "/") {
		return "instance_mismatch"
	}
	if metadata.Principal != principal {
		return "principal_mismatch"
	}
	return ""
}

func (rt *runtime) inboxDoctorCheck(instance, origin, principal string) doctorCheck {
	worktree, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	executable, _ := os.Executable()
	_, projectMissing, _ := readHarnessFile(filepath.Join(worktree, ".claude", "settings.local.json"))
	_, userMissing, _ := readHarnessFile(hookSettingsPath(home, "claude"))
	if projectMissing && userMissing {
		return doctorCheck{Name: "inbox-hook", Status: "ok", Detail: "generic Claude inbox hook not installed"}
	}
	code := inboxHookCheck(worktree, home, executable, instance)
	if code == "" {
		paired := pairedBindingCheck(origin, principal)
		if paired == "instance_mismatch" || paired == "principal_mismatch" {
			code = paired
		}
	}
	if code != "" {
		return doctorCheck{Name: "inbox-hook", Status: "warn", Detail: hookbinding.Reason(code)}
	}
	return doctorCheck{Name: "inbox-hook", Status: "ok", Detail: "generic hook matches this CLI; indexed registrations select their own instance"}
}

// The private, live source index decides the generic hook's account. Only
// public selectors are read here: credentials stay in normal CLI resolution.
// Explicit env bindings and explicit conflicting instances never get replaced.
func (rt *runtime) selectIndexedHook(ctx context.Context, vendor, session string) (bool, error) {
	if hookSessionEnvSet() || !validUUID(vendor) {
		return true, nil
	}
	id, _, dir, result := lookupSessionIndexState(vendor)
	if result != sessionIndexBound || id != session {
		return false, &hookbinding.Refusal{Code: "hook_index_mismatch"}
	}
	raw, err := readOwnerFile(filepath.Join(dir, "state.json"), 1<<20)
	var disk heartbeatDisk
	if err != nil || json.Unmarshal(raw, &disk) != nil {
		return false, &hookbinding.Refusal{Code: "hook_index_mismatch"}
	}
	// Registrations made before this selector was recorded retain their
	// legacy behaviour. No account is inferred from labels or foreign state.
	if disk.HookOrigin == "" {
		return true, nil
	}
	if rt.instance != "" && rt.instance != disk.HookInstance {
		return false, &hookbinding.Refusal{Code: "hook_instance_mismatch"}
	}
	next := *rt
	next.instance, next.configPath = disk.HookInstance, disk.HookConfig
	api, err := next.api()
	if err != nil {
		return false, errors.New("indexed hook account unavailable")
	}
	if strings.TrimRight(api.BaseURL, "/") != disk.HookOrigin {
		return false, &hookbinding.Refusal{Code: "hook_instance_mismatch"}
	}
	me, err := api.Me(ctx)
	if err != nil || me.Principal.ID != disk.HookPrincipal {
		return false, &hookbinding.Refusal{Code: "principal_mismatch"}
	}
	rt.instance, rt.configPath = next.instance, next.configPath
	paired := pairedBindingCheck(disk.HookOrigin, disk.HookPrincipal)
	return paired != "instance_mismatch" && paired != "principal_mismatch", nil
}
