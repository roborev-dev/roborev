{
  description = "roborev - automatic code review daemon for git commits";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages = {
          default = pkgs.buildGoModule {
            pname = "roborev";
            version = "0.13.2";

            src = ./.;

            vendorHash = "sha256-d+z2isDa3wQhwM3glu2T2zl/z6yjAxa4Of3kZTXgkJQ=";

            subPackages = [ "cmd/roborev" ];

            nativeCheckInputs = [ pkgs.git ];

            meta = with pkgs.lib; {
              description = "Automatic code review daemon for git commits";
              homepage = "https://github.com/roborev-dev/roborev";
              license = licenses.mit;
              mainProgram = "roborev";
            };
          };
        };

        apps = {
          default = flake-utils.lib.mkApp {
            drv = self.packages.${system}.default;
            exePath = "/bin/roborev";
          };
          roborev = self.apps.${system}.default;
        };

        formatter = pkgs.nixfmt;

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
          ];
        };
      }
    );
}
