{
  description = "Prebuilt c2j extension ops";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = inputs@{ nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      packages = forAllSystems (system:
        import ./nix/packages.nix {
          pkgs = nixpkgs.legacyPackages.${system};
        });
      checks = forAllSystems (system:
        import ./nix/checks.nix {
          pkgs = nixpkgs.legacyPackages.${system};
          packages = inputs.self.packages.${system};
        });
      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [ go gitMinimal gnumake python3 uv nodejs_24 pnpm ];
            PNPM_CONFIG_ALLOW_BUILDS = builtins.readFile ./nix/pnpm-build-policy.json;
          };
        });
    };
}
