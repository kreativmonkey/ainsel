{
  description = "AInsel development tools";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { nixpkgs, ... }:
    let
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ] (
          system: f nixpkgs.legacyPackages.${system}
        );
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_26
            golangci-lint
            nodejs_24
            corepack
            kubernetes-helm
            kubectl
            kubelogin-oidc
            gnumake
            nginx
            shellcheck
            (python3.withPackages (ps: [ ps.pyyaml ]))
            just
          ];
          shellHook = ''echo "AInsel dev shell — run just"'';
        };
      });
      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
