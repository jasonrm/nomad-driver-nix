{
  lib,
  buildGoModule,
  rev ? null,
}:

buildGoModule (finalAttrs: {
  pname = "nomad-driver-nix";
  version =
    let
      base = (builtins.fromJSON (builtins.readFile ./metadata.json)).version;
    in
    if rev != null then "${base}+git.${rev}" else base;

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./main.go
      ./metadata.json
      ./nix
    ];
  };

  vendorHash = "sha256-MZLw0IpixVLAjzhCuGW7UJbg3/MgVw0XsmS7bV+K9Ig=";

  subPackages = [ "." ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${finalAttrs.version}"
  ];

  meta = {
    description = "Nix task driver for HashiCorp Nomad";
    homepage = "https://github.com/jasonrm/nomad-driver-nix";
    license = lib.licenses.mpl20;
    mainProgram = "nomad-driver-nix";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
})
