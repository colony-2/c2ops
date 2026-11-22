{ pkgs }:
let
  pins = {
    x86_64-linux = { platform = "linux_amd64"; hash = "17e2a6ede803b4a6d2cf52844cdcc877a02700bd62ae5451c0c8343a79992048"; };
    aarch64-linux = { platform = "linux_arm64"; hash = "d6cb1fb105a92574360dd0b4cccbeb255d93a02e39ce513cbe9cd8d12a865109"; };
    aarch64-darwin = { platform = "darwin_arm64"; hash = "07038f7f9b4e417675c7ac9fd0a084845215f14c0e4a073934666ea0f6511333"; };
  };
  pin = pins.${pkgs.stdenv.hostPlatform.system};
in pkgs.fetchurl {
  url = "https://github.com/opentofu/terraform-provider-google/releases/download/v7.10.0/terraform-provider-google_7.10.0_${pin.platform}.zip";
  sha256 = pin.hash;
}
