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
      ./patches
    ];
  };

  vendorHash = "sha256-/12rGg+wuRDP7iBjnQcInZxBqC8d0+kAC+hkVWTrSqA=";

  # Apply after buildGoModule installs vendor/, not while producing goModules.
  # This keeps the dependency hash unchanged and patches only our binary.
  postConfigure = ''
    chmod -R u+w vendor/github.com/hashicorp/nomad/drivers/shared/executor
    patch --batch --fuzz=0 -p1 < patches/executor-shutdown-diagnostics.patch
    cp patches/shutdown_diagnostics{,_linux}.go \
      vendor/github.com/hashicorp/nomad/drivers/shared/executor/
  '';

  subPackages = [ "." ];
  postCheck = ''
    go test ./patches
  '';

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
