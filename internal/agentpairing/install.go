// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/config"
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

var pairHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validPairOrigin accepts an http(s) origin: scheme, host, and optional port.
// Bracketed IPv6 hosts are included. Userinfo, paths, queries, and fragments are
// refused before a command is built so a crafted URL cannot become shell.
func validPairOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Host == "" {
		return false
	}
	if origin != u.Scheme+"://"+u.Host || strings.HasSuffix(u.Host, ":") {
		return false
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return false
		}
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.HasPrefix(u.Host, "[") {
		ip := net.ParseIP(host)
		if ip == nil || !strings.Contains(host, ":") {
			return false
		}
		want := "[" + host + "]"
		if port != "" {
			want += ":" + port
		}
		return u.Host == want
	}
	if strings.Contains(host, ":") || !pairHostnamePattern.MatchString(host) {
		return false
	}
	want := host
	if port != "" {
		want += ":" + port
	}
	return u.Host == want
}

// pairNextLine is the stdout line when ~/.local/bin is already on PATH.
// The origin is shell-quoted. Anything that is not an origin is refused.
func pairNextLine(origin string) (string, bool) {
	if !validPairOrigin(origin) {
		return "", false
	}
	return "aeon-agentd pair --url " + shellQuote(origin), true
}

// PAIR4's release contract builds four agentd targets. Build availability does
// not assert lifecycle qualification on the user's OS/service manager.
func installTargets(origin string) []InstallTarget {
	if !regexp.MustCompile(`^[0-9]{12}\.0\.0$`).MatchString(version.Version) {
		return []InstallTarget{}
	}
	originOK := validPairOrigin(origin)
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
			// only the pair command. When that directory is absent from PATH the
			// command is the shell-quoted absolute link path, and stderr has one
			// hint to add the directory. The origin is shell-quoted in the script.
			cmd = fmt.Sprintf(`(
set -eu
if command -v aeon-agentd >/dev/null 2>&1; then
  printf 'Aeon is already installed. Use aeon-agentd pair with the --url from this instance guide.\n' >&2
  exit 1
fi
if command -v paimos-agentd >/dev/null 2>&1 || command -v nix >/dev/null 2>&1; then
  printf 'Use this instance guide and the owning Nix / Home Manager configuration to pair with a verified compatible Aeon tool; managed binaries and services are preserved.\n' >&2
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
aeon_shell_quote() {
  aeon_rest=$1
  aeon_out=
  while [ -n "$aeon_rest" ]; do
    case "$aeon_rest" in
      *\'*)
        aeon_chunk=${aeon_rest%%\'*}
        aeon_out="$aeon_out'$aeon_chunk'\\'"
        aeon_rest=${aeon_rest#*\'}
        ;;
      *)
        aeon_out="$aeon_out'$aeon_rest'"
        aeon_rest=
        ;;
    esac
  done
  printf '%%s' "$aeon_out"
}
aeon_pair_url=$(aeon_shell_quote %s)
case ":${PATH-}:" in
  *":$HOME/.local/bin:"*)
    printf '%%s\n' "aeon-agentd pair --url $aeon_pair_url"
    ;;
  *)
    aeon_pair_bin=$(aeon_shell_quote "$HOME/.local/bin/aeon-agentd")
    printf '%%s\n' "$aeon_pair_bin pair --url $aeon_pair_url"
    printf '%%s\n' "Add $(aeon_shell_quote "$HOME/.local/bin") to PATH to run aeon-agentd by name." >&2
    ;;
esac
)`, statOwner, statMode, version.Version, version.Version, p.os, p.arch, asset, shellQuote(base+asset), shellQuote(base+"SHA256SUMS"), shellQuote(asset), checksum, asset, shellQuote(origin))
		}
		targets = append(targets, InstallTarget{p.os, p.arch, p.service, "candidate; consult this exact release's service qualification evidence", base + asset, base + "SHA256SUMS", cmd})
	}
	return targets
}

func setupCommand(origin string) string {
	return "aeon-agentd pair --url " + shellQuote(origin)
}

func homebrewCommand(origin string) string {
	return "brew install inspr-at/tap/aeon-agentd\n" + setupCommand(origin)
}

type ManagedSetup struct {
	Command          string `json:"command"`
	ServiceOption    string `json:"service_option"`
	ModuleURL        string `json:"module_url"`
	ServiceNote      string `json:"service_note"`
	PlatformNote     string `json:"platform_note,omitempty"`
	PrerequisiteNote string `json:"prerequisite_note,omitempty"`
}

func managedSetup(origin string, guides ...*config.PairingNixGuide) *ManagedSetup {
	if len(guides) == 0 || guides[0] == nil || guides[0].Validate() != nil {
		return nil
	}
	g := guides[0]
	platform := "Service module: macOS and Linux."
	if len(g.Platforms) == 1 {
		platform = "Service module: macOS only."
		if g.Platforms[0] == "linux" {
			platform = "Service module: Linux only."
		}
	}
	return &ManagedSetup{
		Command:          "aeon-agentd pair --url " + shellQuote(origin),
		ServiceOption:    g.ServiceOption,
		ModuleURL:        g.ModuleURL,
		ServiceNote:      g.ServiceNote,
		PlatformNote:     platform,
		PrerequisiteNote: "Use aeon-agentd on PATH from a reviewed release pin with pair; a service module alone does not ensure this.",
	}
}
