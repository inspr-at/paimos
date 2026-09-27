// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/version"
)

type InstallTarget struct {
	Platform      string `json:"platform"`
	Arch          string `json:"arch"`
	Service       string `json:"service"`
	Qualification string `json:"qualification"`
	ArtifactURL   string `json:"artifact_url"`
	ChecksumsURL  string `json:"checksums_url"`
	Command       string `json:"command"`
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// PAIR4's release contract builds four agentd targets. Build availability does
// not assert lifecycle qualification on the user's OS/service manager.
func installTargets() []InstallTarget {
	if !regexp.MustCompile(`^[0-9]{12}\.0\.0$`).MatchString(version.Version) {
		return []InstallTarget{}
	}
	base := "https://github.com/inspr-at/paimos/releases/download/v" + version.Version + "/"
	targets := []InstallTarget{}
	for _, p := range []struct{ os, arch, service string }{{"darwin", "arm64", "launchd-user"}, {"darwin", "amd64", "launchd-user"}, {"linux", "arm64", "systemd-user"}, {"linux", "amd64", "systemd-user"}} {
		asset := "paimos-agentd-" + p.os + "-" + p.arch
		checksum := "sha256sum -c pairing-checksum.txt"
		if p.os == "darwin" {
			checksum = "shasum -a 256 -c pairing-checksum.txt"
		}
		// An exclusive versioned directory prevents overwrite and keeps downloaded
		// bytes non-executable until the exact manifest entry verifies successfully.
		cmd := fmt.Sprintf(`(
set -eu
if command -v paimos-agentd >/dev/null 2>&1 || command -v nix >/dev/null 2>&1; then
  printf 'Existing or Nix-managed installation: reuse the verified tool or use the owning declarative configuration.\n' >&2
  exit 1
fi
umask 077
aeon_pairing_dir="$HOME/.local/lib/aeon/%s/%s-%s"
if [ -e "$aeon_pairing_dir" ] || [ -L "$aeon_pairing_dir" ]; then
  printf 'Versioned destination already exists; inspect/reuse it, never overwrite.\n' >&2
  exit 1
fi
mkdir -p "$HOME/.local/lib/aeon/%s"
mkdir "$aeon_pairing_dir"
cd "$aeon_pairing_dir"
curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output %s %s
curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output SHA256SUMS %s
awk '$2 == "%s" { print; n++ } END { if (n != 1) exit 1 }' SHA256SUMS > pairing-checksum.txt
%s
install -m 0700 %s "$aeon_pairing_dir/paimos-agentd"
printf 'Verified binary: %%s/paimos-agentd\n' "$aeon_pairing_dir"
)`, version.Version, p.os, p.arch, version.Version, asset, shellQuote(base+asset), shellQuote(base+"SHA256SUMS"), asset, checksum, asset)
		targets = append(targets, InstallTarget{p.os, p.arch, p.service, "candidate; consult this exact release's service qualification evidence", base + asset, base + "SHA256SUMS", cmd})
	}
	return targets
}

func setupCommand(origin string) string {
	return `<verified absolute paimos-agentd path> setup --url ` + shellQuote(origin) + ` --workspace <absolute approved folder> --state-root <absolute private folder outside repos> --harness <codex|claude|cursor|grok> --start-service`
}
