{ pkgs }:
let
  inherit (pkgs) lib;
  catalog = builtins.fromJSON (builtins.readFile ./ops.json);
  vendorHashes = builtins.fromJSON (builtins.readFile ./go-vendor-hashes.json);

  pythonRuntime = pkgs.python3.override {
    packageOverrides = final: prev: {
      litellm = prev.litellm.overridePythonAttrs (old: {
        patches = (old.patches or [ ]) ++ [ ./patches/litellm-finalization.patch ];
      });
    };
  };
  aiderPackage = pkgs.aider-chat.override { python3Packages = pythonRuntime.pkgs; };

  # Reuse nixpkgs derivations, with only the compatibility fixes above/below.
  # withPackages links libraries into an environment; it does not vendor them.
  pythonEnvs = {
    pydantic = pythonRuntime.withPackages (p: [
      (p.callPackage ./pydantic-ai.nix { })
      (p.callPackage ./anthropic-compat.nix { })
      p.openai p.tiktoken p.google-genai
    ]);
    litellm = pythonRuntime.withPackages (p: [ p.litellm ]);
    jev = pythonRuntime.withPackages (p: [ (p.callPackage ./typesafe-sdk.nix { }) ]);
    aider = pythonRuntime;
    kimi = pythonRuntime;
  };

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
        src = lib.fileset.toSource {
          root = ../. + "/${module}";
          # Exclude empty test/cache directories as well as their files, so a
          # path override and a Git checkout produce the same package.
          fileset = lib.fileset.fileFilter (file:
            (file.hasExt "go" && !(lib.hasSuffix "_test.go" file.name)) ||
            builtins.elem file.name [ "go.mod" "go.sum" ]
          ) (../. + "/${module}");
        };
        vendorHash = vendorHashes.${module};
        subPackages = [ subPackage ];
        env.CGO_ENABLED = "0";
        ldflags = [ "-s" "-w" ];
        doCheck = false;
      };
      python = pythonEnvs.${name};
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
          ${lib.optionalString (name == "aider") "--set C2OPS_AIDER_BIN ${lib.getExe aiderPackage}"} \
          --prefix PATH : ${lib.makeBinPath ([ python ] ++ runtime)}
      '') + ''
        runHook postInstall
      '';
      passthru.c2j = manifest;
      passthru.pythonEnv = if op.kind == "python" then python else null;
      meta = {
        description = manifest.description;
        mainProgram = name;
        platforms = [ "x86_64-linux" "aarch64-linux" ];
        license = lib.licenses.asl20;
      };
    };
in (lib.mapAttrs mkOp catalog) // {
  # A CLI dependency for source/Git Aider ops, not a JSON-speaking c2j op.
  # It is also in the packaged Aider op's closure, so CI publishes it there.
  aider-cli = aiderPackage;
}
