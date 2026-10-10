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

      # The release workflow passes the tag in (`nix build --impure`); a pure
      # build is stamped with the commit instead.
      version =
        let
          tag = builtins.getEnv "ALERTS_VERSION";
        in
        if tag != "" then tag else "dev-${self.shortRev or self.dirtyShortRev or "unknown"}";

      # Only what the Go build reads, so documentation changes do not rebuild.
      source =
        pkgs:
        pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./go.mod
            ./go.sum
            ./vendor
            ./cmd
            ./internal
          ];
        };
    in
    {
      packages = forAllSystems (pkgs: rec {
        # The binary for this machine. Building it runs the whole test suite
        # in the sandbox.
        alerts = pkgs.buildGoModule {
          pname = "alerts";
          inherit version;
          src = source pkgs;
          vendorHash = null; # vendor/ is committed
          env.CGO_ENABLED = "0";
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];
          # Test scripts run with the minimal PATH alerts gives every command,
          # and the sandbox has no /usr/bin: point them at coreutils.
          preCheck = ''
            export ALERTS_TEST_TOOLS=${pkgs.lib.makeBinPath [ pkgs.coreutils ]}
          '';
        };
        default = alerts;

        # What a GitHub Release carries: static binaries for both target
        # architectures, cross-compiled by Go itself, plus their checksums.
        release = pkgs.stdenvNoCC.mkDerivation {
          pname = "alerts-release";
          inherit version;
          src = source pkgs;
          nativeBuildInputs = [ pkgs.go ];
          buildPhase = ''
            runHook preBuild
            export HOME=$TMPDIR GOCACHE=$TMPDIR/go-cache
            export GOFLAGS=-mod=vendor GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux
            mkdir -p $out
            for arch in amd64 arm64; do
              GOARCH=$arch go build -trimpath \
                -ldflags "-s -w -X main.version=${version}" \
                -o $out/alerts_linux_$arch ./cmd/alerts
            done
            (cd $out && sha256sum alerts_linux_* > SHA256SUMS)
            runHook postBuild
          '';
          dontInstall = true;
          dontFixup = true; # foreign-architecture binaries: nothing to patch or strip
        };
      });

      checks = forAllSystems (pkgs: {
        inherit (self.packages.${pkgs.stdenv.hostPlatform.system}) alerts release;
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
