{
  description = "alerts — run monitoring-plugins checks and deliver alerts";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      git-hooks,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      checks = forAllSystems (pkgs: {
        pre-commit = git-hooks.lib.${pkgs.stdenv.hostPlatform.system}.run {
          src = ./.;
          # Third-party code is vendored so the sandboxed hooks build offline;
          # it is not ours to lint.
          excludes = [ "^vendor/" ];
          hooks = {
            gofmt.enable = true;
            govet.enable = true;
            # staticcheck and golangci-lint shell out to `go`, which the
            # hook environment does not carry on its own.
            staticcheck = {
              enable = true;
              extraPackages = [ pkgs.go ];
            };
            golangci-lint = {
              enable = true;
              name = "golangci-lint";
              entry = "${pkgs.golangci-lint}/bin/golangci-lint run";
              types = [ "go" ];
              pass_filenames = false;
              extraPackages = [ pkgs.go ];
            };
            shellcheck = {
              enable = true;
              # direnv syntax, not a shell script.
              excludes = [ "^\\.envrc$" ];
            };
            check-yaml.enable = true;
            nixfmt.enable = true;
            end-of-file-fixer.enable = true;
            trim-trailing-whitespace.enable = true;
            commitizen.enable = true;
          };
        };
      });

      devShells = forAllSystems (pkgs: {
        # mkShell rather than mkShellNoCC: `go test -race` needs a C toolchain.
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.golangci-lint
            pkgs.just
            pkgs.gh
          ];
          buildInputs = self.checks.${pkgs.stdenv.hostPlatform.system}.pre-commit.enabledPackages;
          shellHook = self.checks.${pkgs.stdenv.hostPlatform.system}.pre-commit.shellHook;
        };
      });
    };
}
