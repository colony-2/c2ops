{
  description = "Prebuilt c2j extension ops";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    pyproject-nix = {
      url = "github:pyproject-nix/pyproject.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    uv2nix = {
      url = "github:pyproject-nix/uv2nix";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.pyproject-nix.follows = "pyproject-nix";
    };
    pyproject-build-systems = {
      url = "github:pyproject-nix/build-system-pkgs";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.pyproject-nix.follows = "pyproject-nix";
      inputs.uv2nix.follows = "uv2nix";
    };
  };

  outputs = inputs@{ nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      packages = forAllSystems (system:
        import ./nix/packages.nix {
          inherit inputs;
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
            packages = with pkgs; [ go gitMinimal gnumake python312 uv nodejs_24 pnpm ];
            PNPM_CONFIG_ALLOW_BUILDS = builtins.readFile ./nix/pnpm-build-policy.json;
          };
        });
    };
}
