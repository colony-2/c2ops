{ pkgs, packages }:
let
  inherit (pkgs) lib;
in {
  manifests = pkgs.runCommand "c2ops-manifests" {
    nativeBuildInputs = [ (pkgs.python313.withPackages (p: [ p.pyyaml p.jsonschema ])) ];
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
    nativeBuildInputs = [ pkgs.python313 ];
  } ''
    python ${../scripts/check-package.py} ${package} ${pkgs.writeText "${name}-manifest.json" (builtins.toJSON package.c2j)}
    touch "$out"
  ''
) packages
