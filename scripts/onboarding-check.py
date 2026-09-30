# SPDX-License-Identifier: AGPL-3.0-only
"""Check that release builds, checksums, publication, and guide agree."""

from pathlib import Path
import re


ROOT = Path(__file__).resolve().parents[1]
release = (ROOT / ".github/workflows/release.yml").read_text()
build = (ROOT / "scripts/build-release-binaries.sh").read_text()
platform = (ROOT / ".github/workflows/pairing-platform.yml").read_text()
guide = (ROOT / "docs/AGENT_INTEGRATION.md").read_text()

pairs = {"darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64"}
agent_assets = {"paimos-agentd-" + pair.replace("/", "-") for pair in pairs}
cli_assets = {"aeon-cli-" + pair.replace("/", "-") for pair in pairs}
all_assets = agent_assets | cli_assets


def require(ok: bool, message: str) -> None:
    if not ok:
        raise SystemExit("onboarding check: " + message)


loops = re.findall(r"for pair in ([^;]+); do", build)
require(len(loops) == 1 and set(loops[0].split()) == pairs, "CLI build target drift")
linux = build.split("linux_agentd() {", 1)[1].split("cli_bins() {", 1)[0]
require("for arch in arm64 amd64; do" in linux, "Linux agentd build target drift")
for runner, arch in (("macos-15", "arm64"), ("macos-15-intel", "amd64")):
    require(f"runner: {runner}\n            arch: {arch}" in release, "Darwin agentd build target drift")
for mode in ("darwin-agentd", "linux-agentd", "cli", "verify-darwin"):
    require("bash scripts/build-release-binaries.sh " + mode in release, "missing release build mode: " + mode)

checksum_section = release.split("sha256sum \\\n", 1)[1].split("> SHA256SUMS", 1)[0]
checksum_assets = set(re.findall(r"\b(?:paimos-agentd|aeon-cli)-(?:darwin|linux)-(?:arm64|amd64)\b", checksum_section))
require(checksum_assets == all_assets, "SHA256SUMS asset drift")
require(len(re.findall(r"\b(?:paimos-agentd|aeon-cli)-(?:darwin|linux)-(?:arm64|amd64)\b", checksum_section)) == 8, "duplicate checksum asset")

create_section = release.split('gh release create "$tag"', 1)[1].split('--title "$tag"', 1)[0]
published = re.findall(r"dist/((?:paimos-agentd|aeon-cli)-(?:darwin|linux)-(?:arm64|amd64)|SHA256SUMS)", create_section)
require(set(published) == all_assets | {"SHA256SUMS"} and len(published) == 9, "GitHub release asset drift")
require("--draft \\" in create_section and "--verify-tag \\" in create_section, "release must remain a draft on the existing tag")
require("gh release upload" not in release and "--clobber" not in release, "release replacement path")
require(release.index("Reject an existing GitHub release") < release.index("Build and push"), "immutability guard must precede publication")
require(release.index("Reject an existing image tag") < release.index("Build and push"), "image immutability guard must precede publication")
require('gh api --paginate "repos/${GITHUB_REPOSITORY}/releases?per_page=100"' in release, "release guard must include drafts and every page")
require("orgs/inspr-at/packages/container/aeon/versions?per_page=100" in release, "image guard package drift")
agentd_note = next((line for line in release.splitlines() if '"paimos-agentd ${VERSION} for ' in line), "")
for pair in pairs:
    require(pair.replace("/", "-") in agentd_note, "agentd release notes omit " + pair)

for asset in agent_assets:
    require(asset in guide, "guide missing " + asset)
require("https://github.com/inspr-at/paimos/releases/download/v$VERSION" in guide, "guide points to wrong release repository")
require("selected.SHA256SUMS" in guide and "sha256sum -c" in guide and "shasum -a 256 -c" in guide, "guide lacks checksum verification")
require("curl|sh" not in guide and "/releases/latest" not in guide, "unsafe bootstrap in guide")

for runner in ("macos-15", "macos-15-intel", "ubuntu-24.04", "ubuntu-24.04-arm"):
    require("runner: " + runner in platform, "platform runner missing: " + runner)
require("AEON_DISPOSABLE_SERVICE_FIXTURE" in platform, "CI fixture guard missing")

print("onboarding check: four build targets, eight checksums, nine release assets, pinned guide, four CI runners")
