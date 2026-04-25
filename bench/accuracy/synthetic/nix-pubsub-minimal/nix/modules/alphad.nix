{ config, lib, pkgs, ... }:
with lib;
let
  cfg = config.redacted.services.alphad;
in
{
  options.redacted.services.alphad = {
    enable = mkEnableOption "alphad";
    baf.pub_topic = mkOption {
      type = types.str;
      description = "baf publisher topic";
      default = "alphad";
    };
    baf.sub_topics = mkOption {
      type = types.listOf types.str;
      description = "baf subscriber topics";
      default = [ "broker-a" "broker-b" "telemetry" ];
    };
  };

  config = mkIf cfg.enable {
    systemd.services.alphad = {
      description = "Alpha synthetic test daemon";
      script = ''
        set -aeuo pipefail
        ${pkgs.redacted.submsg}/bin/submsg ${builtins.concatStringsSep " " cfg.baf.sub_topics} \
          | ${pkgs.redacted.alphad}/bin/alphad \
          | ${pkgs.redacted.pubmsg}/bin/pubmsg ${cfg.baf.pub_topic}
      '';
    };
  };
}
