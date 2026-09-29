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

var pairOriginPattern = regexp.MustCompile(`^https?://[A-Za-z0-9._:-]+$`)

// pairNextLine is the installer's only stdout line. The origin is the serving
// instance (scheme and host). Anything else is refused before a command is built
// so a crafted URL cannot become shell.
func pairNextLine(origin string) (string, bool) {
	if !pairOriginPattern.MatchString(origin) {
		return "", false
	}
	return "aeon-agentd pair --url " + origin, true
}

// PAIR4's release contract builds four agentd targets. Build availability does
// not assert lifecycle qualification on the user's OS/service manager.
func installTargets(origin string) []InstallTarget {
	if !regexp.MustCompile(`^[0-9]{12}\.0\.0$`).MatchString(version.Version) {
		return []InstallTarget{}
	}
	line, originOK := pairNextLine(origin)
	base := "https://github.com/inspr-at/paimos/releases/download/v" + version.Version + "/"
	targets := []InstallTarget{}
	for _, p := range []struct{ os, arch, service string }{{"darwin", "arm64", "launchd-user"}, {"darwin", "amd64", "launchd-user"}, {"linux", "arm64", "systemd-user"}, {"linux", "amd64", "systemd-user"}} {
		asset := "paimos-agentd-" + p.os + "-" + p.arch
		checksum := "sha256sum -c selected.SHA256SUMS >checksum.out 2>checksum.err || { cat checksum.err >&2; cat checksum.out >&2; exit 1; }\nrm -f checksum.out checksum.err"
		statOwner, statMode := "stat -c %u", "stat -c %a"
		if p.os == "darwin" {
			checksum = "shasum -a 256 -c selected.SHA256SUMS >checksum.out 2>checksum.err || { cat checksum.err >&2; cat checksum.out >&2; exit 1; }\nrm -f checksum.out checksum.err"
			statOwner, statMode = "stat -f %u", "stat -f %Lp"
		}
		cmd := "(\nset -eu\nprintf 'Refusing to install without a usable instance URL.\\n' >&2\nexit 1\n)\n"
		if originOK {
			// An exclusive versioned directory prevents overwrite and keeps downloaded
			// bytes non-executable until the exact manifest entry verifies successfully.
			// The verified file is then linked at ~/.local/bin/aeon-agentd. stdout is
			// only the pair command for this instance.
			cmd = fmt.Sprintf(`(
set -eu
if command -v paimos-agentd >/dev/null 2>&1 || command -v nix >/dev/null 2>&1; then
  printf 'Existing or Nix-managed installation: reuse the verified tool or use the owning declarative configuration.\n' >&2
  exit 1
fi
umask 077
case "$HOME" in
  /*) ;;
  *) printf 'HOME must be an absolute directory.\n' >&2; exit 1 ;;
esac
case "$HOME" in
  */|*/./*|*/../*|*/.|*/..|*//*) printf 'HOME must not contain ambiguous path components.\n' >&2; exit 1 ;;
esac
check_private_dir() {
  if [ -L "$1" ] || [ ! -d "$1" ]; then
    printf 'Unsafe installation directory.\n' >&2
    exit 1
  fi
  aeon_owner=$(%s "$1")
  aeon_mode=$(%s "$1")
  case "$aeon_mode" in
    ''|*[!0-7]*) printf 'Unsafe installation permissions.\n' >&2; exit 1 ;;
  esac
  if [ "$aeon_owner" != "$(id -u)" ] || [ "$((0$aeon_mode & 022))" -ne 0 ]; then
    printf 'Installation directory must be owned by you and not writable by others.\n' >&2
    exit 1
  fi
}
ensure_private_dir() {
  if [ ! -e "$1" ] && [ ! -L "$1" ]; then
    mkdir "$1"
  fi
  check_private_dir "$1"
}
check_private_dir "$HOME"
ensure_private_dir "$HOME/.local"
ensure_private_dir "$HOME/.local/bin"
aeon_bin="$HOME/.local/bin/aeon-agentd"
if [ -L "$aeon_bin" ]; then
  aeon_link=$(readlink "$aeon_bin") || exit 1
  case "$aeon_link" in
    "$HOME/.local/lib/aeon/"*) ;;
    *) printf 'Refusing to replace an existing aeon-agentd.\n' >&2; exit 1 ;;
  esac
  case "$aeon_link" in
    *..*) printf 'Refusing to replace an existing aeon-agentd.\n' >&2; exit 1 ;;
  esac
elif [ -e "$aeon_bin" ]; then
  printf 'Refusing to replace an existing aeon-agentd.\n' >&2
  exit 1
fi
ensure_private_dir "$HOME/.local/lib"
ensure_private_dir "$HOME/.local/lib/aeon"
ensure_private_dir "$HOME/.local/lib/aeon/%s"
aeon_pairing_dir="$HOME/.local/lib/aeon/%s/%s-%s"
if [ -e "$aeon_pairing_dir" ] || [ -L "$aeon_pairing_dir" ]; then
  printf 'Versioned destination already exists; inspect/reuse it, never overwrite.\n' >&2
  exit 1
fi
mkdir "$aeon_pairing_dir"
cd "$aeon_pairing_dir"
curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output %s %s
curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output SHA256SUMS %s
awk -v asset=%s '$2 == asset && length($1) == 64 && $1 !~ /[^0-9a-f]/ { print; n++ } END { if (n != 1) exit 1 }' SHA256SUMS > selected.SHA256SUMS
%s
install -m 0700 %s "$aeon_pairing_dir/paimos-agentd"
if [ -L "$aeon_bin" ]; then
  ln -sfn "$aeon_pairing_dir/paimos-agentd" "$aeon_bin"
else
  ln -s "$aeon_pairing_dir/paimos-agentd" "$aeon_bin"
fi
printf '%%s\n' %s
)`, statOwner, statMode, version.Version, version.Version, p.os, p.arch, asset, shellQuote(base+asset), shellQuote(base+"SHA256SUMS"), shellQuote(asset), checksum, asset, shellQuote(line))
		}
		targets = append(targets, InstallTarget{p.os, p.arch, p.service, "candidate; consult this exact release's service qualification evidence", base + asset, base + "SHA256SUMS", cmd})
	}
	return targets
}

func setupCommand(origin string) string {
	return `<verified absolute paimos-agentd path> setup --url ` + shellQuote(origin) + ` --workspace <absolute approved folder> --state-root <absolute private folder outside repos> --harness <codex|claude|cursor|grok> --start-service`
}
