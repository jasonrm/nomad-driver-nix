{
  description = "A very basic flake";

  outputs = { self, nixpkgs }:
  let
    pkgs = import nixpkgs { system = "x86_64-linux"; };
    hello = pkgs.writeScriptBin "hello" ''
      #!${pkgs.bash}/bin/bash
      echo "Hello from bash script!"
    '';
  in {

    packages.x86_64-linux.hello = hello;

    packages.x86_64-linux.default = self.packages.x86_64-linux.hello;

  };
}
