{ fetchgit }:
let pin = builtins.fromJSON (builtins.readFile ./act-source.json);
in fetchgit {
  name = "act-source";
  inherit (pin) url rev hash;
  fetchSubmodules = false;
}
