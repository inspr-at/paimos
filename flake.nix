# SPDX-License-Identifier: AGPL-3.0-only
# packages.<system>.aeon is a static CGO_ENABLED=0 client build of ./cmd/aeon
# without the webembed tag, and installs bin/paimos as a symlink to bin/aeon.
# packages.<system>.aeon-agentd builds ./cmd/aeon-agentd. Darwin sets
# CGO_ENABLED=1 so the daemon links LocalAuthentication. Linux stays at 0.
{
  description = "PAIMOS AEON";

  # nixos-26.05 still evaluates x86_64-darwin and ships Go 1.26.
  # Unstable 26.11 dropped x86_64-darwin.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      each = f: nixpkgs.lib.genAttrs systems (system: f system (import nixpkgs { inherit system; }));
      version = (builtins.fromJSON (builtins.readFile ./version.json)).version;
      # Pinned by building. proxyVendor avoids a case-insensitive darwin vendor tree.
      vendorHash = "sha256-J60vwwsAGRGx0tMWiOWaWtqnOQYBrb3KI9sxAhmWzfU=";
    in
    {
      packages = each (
        _system: pkgs:
        let
          common = {
            inherit version vendorHash;
            src = ./.;
            proxyVendor = true;
            # Go tests need Postgres (AEON_TEST_DATABASE_URL) and run outside this sandbox.
            doCheck = false;
            ldflags = [
              "-X github.com/inspr-at/paimos/internal/version.Version=${version}"
            ];
            env.CGO_ENABLED = 0;
          };
          license = pkgs.lib.licenses.agpl3Only;
        in
        rec {
          aeon = pkgs.buildGoModule (
            common
            // {
              pname = "aeon";
              subPackages = [ "cmd/aeon" ];
              postInstall = ''
                ln -s aeon "$out/bin/paimos"
              '';
              doInstallCheck = true;
              installCheckPhase = ''
                runHook preInstallCheck
                test -L "$out/bin/paimos"
                "$out/bin/paimos" --help | grep "paimos <command>" >/dev/null
                "$out/bin/aeon" --help | grep "aeon <command>" >/dev/null
                runHook postInstallCheck
              '';
              meta = {
                description = "PAIMOS AEON client; paimos-compatible when invoked as paimos";
                homepage = "https://github.com/inspr-at/paimos";
                inherit license;
                mainProgram = "aeon";
                platforms = systems;
              };
            }
          );
          aeon-agentd = pkgs.buildGoModule (
            common
            // {
              pname = "aeon-agentd";
              subPackages = [ "cmd/aeon-agentd" ];
              meta = {
                description = "PAIMOS AEON local harness supervisor";
                homepage = "https://github.com/inspr-at/paimos";
                inherit license;
                mainProgram = "aeon-agentd";
                platforms = systems;
              };
            }
            // pkgs.lib.optionalAttrs pkgs.stdenv.isDarwin {
              env.CGO_ENABLED = 1;
            }
          );
          default = aeon;
        }
      );

      checks = each (
        system: _pkgs: {
          inherit (self.packages.${system}) aeon aeon-agentd;
        }
      );

      devShells = each (
        _system: pkgs: {
          default = pkgs.mkShell {
            packages = [ pkgs.go ];
          };
        }
      );
    };
}
