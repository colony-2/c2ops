{ pkgs }:
let
  inherit (pkgs) lib;
  catalog = builtins.fromJSON (builtins.readFile ./ops.json);
  vendorHashes = builtins.fromJSON (builtins.readFile ./go-vendor-hashes.json);
  ghaGo = pkgs.callPackage ./gha-go.nix { };
  actPin = builtins.fromJSON (builtins.readFile ./act-source.json);
  actSource = pkgs.callPackage ./act-source.nix { };

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
      p.openai p.tiktoken p.google-genai p.jsonschema
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
      # pnpm's generated launchers call sed, readlink and uname before Node.
      # Minimal worker images need not expose these tools on their ambient PATH.
      runtime = map (attr: pkgs.${attr}) op.runtime
        ++ lib.optionals (lib.any (lib.hasPrefix "pnpm:") (manifest.dependencies or [ ]))
          [ pkgs.gnused pkgs.coreutils ];
      module = op.module or op.path;
      subPackage = op.subPackage or ".";
      binaryName = if subPackage == "." then builtins.baseNameOf module else subPackage;
      buildGoModule = if module == "gha" then pkgs.buildGoModule.override { go = ghaGo; }
        else pkgs.buildGoModule;
      goBinary = buildGoModule ({
        pname = "c2ops-${name}-binary";
        version = "0.1.0";
        # The act fork is prefetched outside the sandbox; test-only c2j/JobDB
        # imports are excluded. No private credentials enter either derivation.
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
      } // lib.optionalAttrs (module == "gha") {
        # Keep even empty GHA-specific hooks out of unrelated derivations.
        postPatch = ''
          grep -Fqx 'replace github.com/nektos/act => github.com/colony-2/act ${actPin.version}' go.mod
        '';
        # Only the dependency derivation acquires source. Its checked vendor
        # output feeds the binary build; both retain the immutable remote module
        # identity. A build-local path must not become a runtime store reference.
        overrideModAttrs = final: previous: {
          postPatch = (previous.postPatch or "") + ''
            export HOME="$TMPDIR/c2ops-build-home"
            mkdir -p "$HOME"
            cp -R ${actSource} .c2ops-act-source
            go mod edit -replace=github.com/nektos/act=./.c2ops-act-source
          '';
        };
        modPostBuild = ''
          for resource in pkg/runner/hashfiles/index.js pkg/runner/res/trampoline.js pkg/schema/workflow_schema.json pkg/schema/action_schema.json LICENSE pkg/container/DOCKER_LICENSE; do
            test -s "vendor/github.com/nektos/act/$resource"
          done
          substituteInPlace vendor/modules.txt --replace-fail \
            '=> ./.c2ops-act-source' '=> github.com/colony-2/act ${actPin.version}'
        '';
        postInstall = ''
          install -Dm644 ${actSource}/LICENSE "$out/share/licenses/act/LICENSE"
          install -Dm644 ${actSource}/pkg/container/DOCKER_LICENSE "$out/share/licenses/act/DOCKER_LICENSE"
        '';
        # Go telemetry must not create /homeless-shelter on unsandboxed builders.
        # A separate temporary HOME also keeps module/build configuration private.
        preBuild = ''
          export HOME="$TMPDIR/c2ops-build-home"
          mkdir -p "$HOME"
        '';
      });
      python = pythonEnvs.${name};
    in pkgs.stdenvNoCC.mkDerivation {
      pname = "c2ops-${name}";
      version = "0.1.0";
      dontUnpack = true;
      nativeBuildInputs = [ pkgs.makeWrapper ];
      # Compilation belongs to the builder. Fail the build if a Go op retains
      # its compiler anywhere in its runtime closure.
      disallowedRequisites = lib.optionals (op.kind == "go")
        ([ pkgs.go ] ++ lib.optionals (module == "gha") [ ghaGo actSource ]);
      installPhase = ''
        runHook preInstall
        install -Dm644 ${manifestPath} "$out/share/c2j/op.json"
      '' + (if op.kind == "go" then ''
        makeWrapper ${goBinary}/bin/${binaryName} "$out/bin/${name}" \
          ${lib.optionalString (module == "codex") "--set-default C2OPS_CODEX_BIN ${lib.getExe pkgs.codex}"} \
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
      passthru.compiledExecutable =
        if op.kind == "go" then "${goBinary}/bin/${binaryName}" else null;
      passthru.pythonEnv = if op.kind == "python" then python else null;
      # Source-test tooling can select the verified compiler without adding it
      # to an operation's runtime closure or changing unrelated Go operations.
      passthru.buildCompiler = if module == "gha" then ghaGo else null;
      meta = {
        description = manifest.description;
        mainProgram = name;
        platforms = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
        license = lib.licenses.asl20;
      };
    };
in (lib.mapAttrs mkOp catalog) // {
  # CLI dependencies for source/Git ops, not JSON-speaking c2j ops.
  # They are also in the packaged ops' closures, so CI publishes them there.
  codex-cli = pkgs.codex;
  aider-cli = aiderPackage;
}
