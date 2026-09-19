{
  description = "oledctl - oledctl works as a middleman between brightnessctl and gammastep.";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
      nixpkgsFor = forAllSystems (system: import nixpkgs { inherit system; });
    in
    {
      packages = forAllSystems (system:
        let
          pkgs = nixpkgsFor.${system};
        in
        {
          default = pkgs.buildGoModule {
            pname = "oledctl";
            version = "0.1.0";
            src = ./.;

            subPackages = [ "cmd/oledctl" ];
            vendorHash = null;

            meta = with pkgs.lib; {
              description = "OLED brightness control wrapper over brightnessctl";
              license = licenses.mit;
              mainProgram = "oledctl";
            };
          };
        });

      # Export an overlay so Nix replaces pkgs.brightnessctl system-wide
      overlays.default = final: prev: {
        brightnessctl = final.writeShellScriptBin "brightnessctl" ''
          export REAL_BRIGHTNESSCTL="${prev.brightnessctl}/bin/brightnessctl"
          exec ${self.packages.${final.system}.default}/bin/oledctl "$@"
        '';
      };

      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.oledctl;
          oledctlPkg = self.packages.${pkgs.system}.default;
        in
        {
          options.services.oledctl = {
            enable = lib.mkEnableOption "oledctl brightnessctl interceptor";
          };

          config = lib.mkIf cfg.enable {
            # Automatically apply the overlay when the service is enabled
            nixpkgs.overlays = [ self.overlays.default ];

            environment.systemPackages = [
              oledctlPkg
              pkgs.gammastep
            ];
          };
        };
    };
}
