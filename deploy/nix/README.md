# Nix

`deploy/nix/flake.nix` builds the binary (with the web UI embedded) and ships a NixOS module. This is the shape every Pocket app will take on a Pocket Box: a package plus `services.pocket.<app>`.

```nix
# flake.nix of the box
inputs.pocket-core.url = "github:abjelosevic88/pocket-homelab-core?dir=deploy/nix";

# configuration.nix
imports = [ inputs.pocket-core.nixosModules.default ];
services.pocket.core = {
  enable = true;
  baseUrl = "https://core.box.local";
  environmentFile = "/run/secrets/pocket-core.env";   # ADMIN_EMAIL / ADMIN_PASSWORD / REGISTRATION_KEY
  settings.HEALTH_INTERVAL = "30s";
};
```

Two hashes in the flake are placeholders (`sha256-AAAA…`). Fill them once:

```sh
nix run nixpkgs#prefetch-npm-deps -- web/package-lock.json   # → npmDepsHash
nix build ./deploy/nix#pocket-core                           # the error shows vendorHash
```

Build and run outside NixOS: `nix run ./deploy/nix` (needs `DATA_DIR` and `BASE_URL`).
