{ pkgs, lib, ... }:

let
  go_1_26_6 = pkgs.go_1_26.overrideAttrs (_: {
    version = "1.26.6";
    src = pkgs.fetchurl {
      url = "https://go.dev/dl/go1.26.6.src.tar.gz";
      hash = "sha256-oHIcVMaIkBRI13rZs+x+p8R0cwdV/4kTgukuy5P/LLE=";
    };
  });
in

{
  packages = with pkgs; [
    cosign
    go_1_26_6
    gotools
    gopls
    shellcheck
  ];

  languages.javascript = {
    enable = true;
    npm = {
      enable = true;
      install.enable = true;
    };
  };

  env = {
    DATA_DIR = "./data";
    STATIC_DIR = "./frontend/dist";
  };

  enterShell = ''
    echo "PAIMOS dev environment"
    echo "  backend:  cd backend && go run ."
    echo "  frontend: cd frontend && npm run dev"
  '';
}
