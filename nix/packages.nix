{ inputs, pkgs }:
let
  inherit (pkgs) lib;
  catalog = builtins.fromJSON (builtins.readFile ./ops.json);
  vendorHashes = builtins.fromJSON (builtins.readFile ./go-vendor-hashes.json);

  pythonEnv = name:
    let
      workspace = inputs.uv2nix.lib.workspace.loadWorkspace {
        workspaceRoot = ./python + "/${name}";
      };
      pythonSet = (pkgs.callPackage inputs.pyproject-nix.build.packages {
        python = pkgs.${catalog.${name}.python or "python313"};
      }).overrideScope (lib.composeManyExtensions [
        inputs.pyproject-build-systems.overlays.wheel
        (workspace.mkPyprojectOverlay { sourcePreference = "wheel"; })
      ]);
    in pythonSet.mkVirtualEnv "c2ops-${name}-python" workspace.deps.default;

  mkOp = name: op:
    let
      opPath = ../. + "/${op.path}";
      manifestPath = opPath + "/op.json";
      manifest = builtins.fromJSON (builtins.readFile manifestPath);
      runtime = map (attr: pkgs.${attr}) op.runtime;
      module = op.module or op.path;
      subPackage = op.subPackage or ".";
      binaryName = if subPackage == "." then builtins.baseNameOf module else subPackage;
      goBinary = pkgs.buildGoModule {
        pname = "c2ops-${name}-binary";
        version = "0.1.0";
        # Test-only c2j/JobDB imports are private and are not runtime inputs.
        # The normal Go suites run separately in CI, with repository credentials.
        src = lib.cleanSourceWith {
          src = ../. + "/${module}";
          filter = path: type:
            type == "directory" ||
            (lib.hasSuffix ".go" path && !(lib.hasSuffix "_test.go" path)) ||
            builtins.elem (builtins.baseNameOf path) [ "go.mod" "go.sum" ];
        };
        vendorHash = vendorHashes.${module};
        subPackages = [ subPackage ];
        env.CGO_ENABLED = "0";
        ldflags = [ "-s" "-w" ];
        doCheck = false;
      };
      python = if name == "kimi" then pkgs.python313 else pythonEnv name;
    in pkgs.stdenvNoCC.mkDerivation {
      pname = "c2ops-${name}";
      version = "0.1.0";
      dontUnpack = true;
      nativeBuildInputs = [ pkgs.makeWrapper ];
      installPhase = ''
        runHook preInstall
        install -Dm644 ${manifestPath} "$out/share/c2j/op.json"
      '' + (if op.kind == "go" then ''
        makeWrapper ${goBinary}/bin/${binaryName} "$out/bin/${name}" \
          --prefix PATH : ${lib.escapeShellArg (lib.makeBinPath runtime)}
      '' else ''
        install -Dm644 ${opPath + "/main.py"} "$out/libexec/main.py"
        makeWrapper ${python}/bin/python3 "$out/bin/${name}" \
          --add-flags "$out/libexec/main.py" \
          --set PYTHONNOUSERSITE 1 \
          --set PYTHONDONTWRITEBYTECODE 1 \
          --prefix PATH : ${lib.makeBinPath ([ python ] ++ runtime)}
      '') + ''
        runHook postInstall
      '';
      passthru.c2j = manifest;
      meta = {
        description = manifest.description;
        mainProgram = name;
        platforms = [ "x86_64-linux" "aarch64-linux" ];
        license = lib.licenses.asl20;
      };
    };
in lib.mapAttrs mkOp catalog
