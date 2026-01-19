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
            version = "0.12.0";

            src = ./.;

            vendorHash = "sha256-YTLqKBSyhwIg2fy4RykkLwY5L9MFpT4yiPj3RWQv5hA=";

            subPackages = [ "cmd/roborev" "cmd/roborevd" ];

            nativeCheckInputs = [ pkgs.git ];

            meta = with pkgs.lib; {
              description = "Automatic code review daemon for git commits";
              homepage = "https://github.com/wesm/roborev";
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
          roborevd = flake-utils.lib.mkApp {
            drv = self.packages.${system}.default;
            exePath = "/bin/roborevd";
          };
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
