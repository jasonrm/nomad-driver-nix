{
  lib,
  buildGoModule,
  version ? (builtins.fromJSON (builtins.readFile ./metadata.json)).version,
  rev ? null,
}:

buildGoModule (finalAttrs: {
  pname = "nomad-driver-nix";
  version = if rev != null then "${version}+git.${rev}" else version;

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

  vendorHash = "sha256-ckeCmjahauOZNsiLGazNLBxe7vNQLZSp8LKCceZ7opQ=";

  subPackages = [ "." ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${finalAttrs.version}"
    "-X main.buildSHA=${if rev != null then rev else "unknown"}"
  ];

  meta = {
    description = "Nix task driver for HashiCorp Nomad";
    homepage = "https://github.com/jasonrm/nomad-driver-nix";
    license = lib.licenses.mpl20;
    mainProgram = "nomad-driver-nix";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
})
