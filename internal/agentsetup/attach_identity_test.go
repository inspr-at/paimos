// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAttachIdentityRecordsNarrowVendorRoot(t *testing.T) {
	for _, test := range []struct{ harness, pkg string }{{"claude", "@anthropic-ai/claude-code"}, {"codex", "@openai/codex"}, {"cursor", "@cursor/agent"}} {
		t.Run(test.harness, func(t *testing.T) {
			root := physicalTemp(t)
			path := filepath.Join(root, ".ai-cli-updates", "package-v1", "lib", "node_modules", test.pkg, "bin", "harness")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			identity := RecordAttachIdentity(test.harness, path, physicalTemp(t))
			if identity == nil {
				t.Fatal("no local identity")
			}
			if identity.Owner != os.Getuid() || !strings.Contains(identity.InstallRoot, "package-*") || !strings.HasSuffix(identity.InstallRoot, test.pkg) {
				t.Fatal("incorrect install root")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			updated := strings.Replace(path, "package-v1", "package-v2", 1)
			if !identity.Matches(updated, info) {
				t.Fatal("auto-update rejected")
			}
			for _, foreign := range []string{strings.Replace(updated, test.pkg, "@foreign/other", 1), strings.Replace(updated, "package-v2", "package-", 1), updated + "/../../../foreign"} {
				if identity.Matches(foreign, info) {
					t.Fatal("foreign root accepted")
				}
			}
			other := *identity
			other.Owner = os.Getuid() + 10000
			if other.Matches(updated, info) {
				t.Fatal("foreign recorded owner accepted")
			}
			other.InstallRoot = "/"
			if other.Matches(updated, info) {
				t.Fatal("filesystem root accepted")
			}
		})
	}
}
func TestAttachUnknownLayoutStaysExactAndUnsafeRootIsNotRecorded(t *testing.T) {
	root := physicalTemp(t)
	path := filepath.Join(root, "wrapper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /foreign/image\n"), 0700); err != nil {
		t.Fatal(err)
	}
	identity := RecordAttachIdentity("claude", path, "")
	info, _ := os.Stat(path)
	if identity == nil || identity.InstallRoot != path || !identity.Matches(path, info) || identity.Matches(path+"-other", info) {
		t.Fatal("wrapper widened trust")
	}
	if RecordAttachIdentity("grok", path, "") != nil {
		t.Fatal("unrequested harness identity recorded")
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if RecordAttachIdentity("claude", path, "") != nil {
		t.Fatal("writable install root recorded")
	}
}
func TestAttachIdentityRecordedAtPairingAndRepairWithoutNewEnrollment(t *testing.T) {
	for _, harness := range []string{"codex", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			e, api, _, options, _ := engineFixture(t)
			defer e.Store.Close()
			options.Candidates[0].Harness = harness
			approveFixture(t, e, api, options)
			config, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil || len(config.AttachIdentities) != 1 {
				t.Fatal("pairing did not record fallback", err)
			}
			before := config
			before.AttachIdentities = nil
			config.AttachIdentities = nil
			raw, _ := json.Marshal(config)
			if err = e.Store.Write(RuntimeName, raw, false); err != nil {
				t.Fatal(err)
			}
			creates := api.createCount
			progress, err := e.AddHarness(t.Context(), options.Candidates)
			if err != nil || !strings.Contains(progress.Action, "Local attach identity recorded") || api.createCount != creates {
				t.Fatal("repair changed enrollment", err)
			}
			after, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil || len(after.AttachIdentities) != 1 {
				t.Fatal("repair did not record fallback", err)
			}
			after.AttachIdentities = nil
			if !reflect.DeepEqual(before, after) {
				t.Fatal("attach repair changed other runtime authority or pins")
			}
		})
	}
}

func TestAttachNativeInstallerVersionsUseOnlyVendorRoot(t *testing.T) {
	for _, test := range []struct{ harness, dir string }{{"claude", "claude"}, {"cursor", "cursor-agent"}} {
		t.Run(test.harness, func(t *testing.T) {
			root := physicalTemp(t)
			path := filepath.Join(root, ".local", "share", test.dir, "versions", "v1", "binary")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("native fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			identity := RecordAttachIdentity(test.harness, path, "")
			info, _ := os.Stat(path)
			if identity == nil || !identity.Matches(strings.Replace(path, "/v1/", "/v2/", 1), info) {
				t.Fatal("native version update refused")
			}
			if identity.Matches(strings.Replace(path, "/"+test.dir+"/", "/foreign/", 1), info) {
				t.Fatal("native root widened beyond vendor")
			}
		})
	}
}
