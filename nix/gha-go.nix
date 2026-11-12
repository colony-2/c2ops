{ go_1_26, fetchurl }:

# The private act fork requires this patch level. Keep the other operations and
# nixpkgs pin unchanged. Source checksum: https://go.dev/dl/?mode=json&include=all
go_1_26.overrideAttrs (_: {
  version = "1.26.10";
  src = fetchurl {
    url = "https://go.dev/dl/go1.26.10.src.tar.gz";
    hash = "sha256-/WPog7ovK/ypPBZGsx6+9j4rHk0rMXyXidl2po2B828=";
  };
})
