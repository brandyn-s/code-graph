{
  description = "code-graph: persistent code knowledge graph MCP server with source-backed evidence";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "0.9.0";
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "code-graph";
          inherit version;
          src = self;

          # cgo: the vendored tree-sitter grammars and the C extractors compile
          # with the stdenv C compiler; no external C libraries are needed.
          env.CGO_ENABLED = "1";

          subPackages = [ "cmd/code-graph" ];
          ldflags = [ "-s" "-w" "-X main.version=${version}" ];

          # Fill after the first `nix build` attempt: Nix prints the correct
          # hash in the mismatch error. Regenerate whenever go.sum changes.
          #   nix build 2>&1 | grep -A1 'got:' | tail -1
          vendorHash = pkgs.lib.fakeHash;

          # The suite needs git and the synthetic fixtures; run it via
          # `go test ./...` outside the sandbox instead.
          doCheck = false;

          meta = with pkgs.lib; {
            description = "Persistent code knowledge graph MCP server: callers, callees, impact, with source-backed evidence";
            homepage = "https://github.com/brandyn-s/code-graph";
            license = licenses.mit;
            mainProgram = "code-graph";
            platforms = platforms.linux ++ platforms.darwin;
          };
        };

        apps.default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/code-graph";
        };

        devShells.default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.golangci-lint pkgs.shellcheck pkgs.python3 pkgs.git ];
        };
      });
}
