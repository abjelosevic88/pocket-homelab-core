{
  description = "Pocket Core — the hub of a Pocket Box (package + NixOS module)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      src = ../..;
      version = "0.1.0";
    in {
      packages = forAll (pkgs: rec {
        # The React bundle, built once and embedded into the Go binary.
        web = pkgs.buildNpmPackage {
          pname = "pocket-core-web";
          inherit version;
          src = "${src}/web";
          # Refresh with: nix run nixpkgs#prefetch-npm-deps -- web/package-lock.json
          npmDepsHash = "sha256-AXC6YqWoYcaV673nEDk/qngUALYMcqPz5Zzv7d95i14=";
          installPhase = ''
            mkdir -p $out
            cp -r dist/* $out/
          '';
        };

        pocket-core = pkgs.buildGoModule {
          pname = "pocket-core";
          inherit version src;
          # Refresh with: nix build .#pocket-core (the error prints the right hash)
          vendorHash = "sha256-/QD+e39BeO06DdNx166/FcRLQeA9j8EJSwpkEl/Tbdc=";
          subPackages = [ "cmd/pocket-core" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X main.version=${version}" ];
          preBuild = ''
            rm -rf web/dist && mkdir -p web/dist
            cp -r ${web}/* web/dist/
          '';
          meta = with pkgs.lib; {
            description = "App registry, event bus, OIDC provider and device pairing for a Pocket Box";
            homepage = "https://github.com/abjelosevic88/pocket-homelab-core";
            license = licenses.mit;
            mainProgram = "pocket-core";
          };
        };
        default = pocket-core;
      });

      # services.pocket.core = { enable = true; baseUrl = "https://core.box.local"; };
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.pocket.core;
          pkg = self.packages.${pkgs.system}.pocket-core;
        in {
          options.services.pocket.core = {
            enable = lib.mkEnableOption "Pocket Core";
            package = lib.mkOption { type = lib.types.package; default = pkg; };
            port = lib.mkOption { type = lib.types.port; default = 8080; };
            host = lib.mkOption { type = lib.types.str; default = "127.0.0.1"; };
            baseUrl = lib.mkOption { type = lib.types.str; description = "Public URL of Core; also the OIDC issuer and the address in pairing QR codes."; };
            dataDir = lib.mkOption { type = lib.types.str; default = "/var/lib/pocket-core"; };
            environmentFile = lib.mkOption { type = lib.types.nullOr lib.types.path; default = null; description = "Optional file with ADMIN_PASSWORD, REGISTRATION_KEY…"; };
            settings = lib.mkOption { type = lib.types.attrsOf lib.types.str; default = { }; description = "Extra environment variables (see docs/configuration.md)."; };
            openFirewall = lib.mkOption { type = lib.types.bool; default = false; };
          };
          config = lib.mkIf cfg.enable {
            users.users.pocket-core = { isSystemUser = true; group = "pocket-core"; home = cfg.dataDir; };
            users.groups.pocket-core = { };
            systemd.services.pocket-core = {
              description = "Pocket Core";
              wantedBy = [ "multi-user.target" ];
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];
              environment = {
                DATA_DIR = cfg.dataDir;
                HOST = cfg.host;
                PORT = toString cfg.port;
                BASE_URL = cfg.baseUrl;
                SECURE_COOKIES = if lib.hasPrefix "https://" cfg.baseUrl then "true" else "false";
              } // cfg.settings;
              serviceConfig = {
                ExecStart = "${cfg.package}/bin/pocket-core";
                User = "pocket-core";
                Group = "pocket-core";
                StateDirectory = "pocket-core";
                StateDirectoryMode = "0750";
                EnvironmentFile = lib.mkIf (cfg.environmentFile != null) cfg.environmentFile;
                Restart = "on-failure";
                RestartSec = 3;
                NoNewPrivileges = true;
                ProtectSystem = "strict";
                ProtectHome = true;
                PrivateTmp = true;
                ReadWritePaths = [ cfg.dataDir ];
              };
            };
            networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ cfg.port ];
          };
        };
    };
}
