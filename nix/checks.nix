{ pkgs, packages }:
let
  inherit (pkgs) lib;
in {
  gha-source = pkgs.runCommand "c2ops-gha-source-contract" {
    nativeBuildInputs = [ pkgs.python3 ];
  } ''
    python - <<'PY'
    import json
    from pathlib import Path
    root = Path("${../.}")
    pin = json.loads((root / "nix/act-source.json").read_text())
    mod = (root / "gha/go.mod").read_text()
    sums = (root / "gha/go.sum").read_text().splitlines()
    assert f"replace {pin['module']} => {pin['replacement']} {pin['version']}" in mod
    assert f"{pin['replacement']} {pin['version']} {pin['sum']}" in sums
    assert f"{pin['replacement']} {pin['version']}/go.mod {pin['goModSum']}" in sums
    assert not (root / "gha/third_party/act").exists(), "retained act source is forbidden"
    assert "go 1.26.10" in mod
    assert "go 1.26.10" in (root / "gha/integration/go.mod").read_text()
    PY
    touch "$out"
  '';
  manifests = pkgs.runCommand "c2ops-manifests" {
    nativeBuildInputs = [ (pkgs.python3.withPackages (p: [ p.pyyaml p.jsonschema ])) ];
  } ''
    cd ${../.}
    python scripts/manifests.py --check
    python - <<'PY'
    import json
    from pathlib import Path
    import jsonschema
    for op in json.loads(Path("nix/ops.json").read_text()).values():
        manifest = json.loads((Path(op["path"]) / "op.json").read_text())
        for key in ("input_schema", "output_schema"):
            jsonschema.Draft202012Validator.check_schema(manifest[key])
    PY
    touch "$out"
  '';
} // lib.mapAttrs (name: package:
  pkgs.runCommand "c2ops-${name}-contract" {
    nativeBuildInputs = [ pkgs.python3 ];
    SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
  } ''
    python ${../scripts/check-package.py} ${package} ${pkgs.writeText "${name}-manifest.json" (builtins.toJSON package.c2j)} nix:github:colony-2/c2ops/published#${name} \
      ${lib.optionalString (package.compiledExecutable != null) "--compiled-executable ${package.compiledExecutable} --system ${pkgs.stdenv.hostPlatform.system}"}
    ${lib.optionalString (builtins.elem name [ "gha" "gha-job" "gha-workflow" ]) ''
      test -s "$(dirname ${package.compiledExecutable})/../share/licenses/act/LICENSE"
      test -s "$(dirname ${package.compiledExecutable})/../share/licenses/act/DOCKER_LICENSE"
    ''}
    ${lib.optionalString (builtins.elem name [ "pydantic" "litellm" "jev" ]) ''
      ${package.pythonEnv}/bin/python ${../scripts/check-python-runtime.py} ${../. + "/${name}/main.py"} ${pkgs.python3Packages.pydantic} ${name}
    ''}
    ${lib.optionalString (name == "pydantic") ''
      PYTHONPATH=${pkgs.python3Packages.pyyaml}/${pkgs.python3.sitePackages} \
        ${package.pythonEnv}/bin/python ${../pydantic}/test_binding.py \
        --module ${package}/libexec/main.py --launcher ${package}/bin/pydantic
    ''}
    ${lib.optionalString (name == "aider") ''
      HOME=$(mktemp -d) timeout 30 ${lib.getExe packages.aider-cli} --version
    ''}
    ${lib.optionalString (builtins.elem name [ "codex" "skill-run" ]) ''
      HOME=$(mktemp -d) timeout 30 ${lib.getExe packages.codex-cli} --version
    ''}
    touch "$out"
  ''
) (lib.getAttrs (builtins.attrNames (builtins.fromJSON (builtins.readFile ./ops.json))) packages)
