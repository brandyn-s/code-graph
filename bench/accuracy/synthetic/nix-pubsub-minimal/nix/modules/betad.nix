{ config, lib, pkgs, ... }:
{
  options.services.betad = {
    enable = lib.mkEnableOption "betad";
  };

  # No mkOption defaults — pure imperative pubmsg literal in script.
  # Tests the canstatd-style pattern.
  config = {
    systemd.services.betad = {
      description = "Beta synthetic daemon (imperative-only pub)";
      script = ''
        set -aeuo pipefail
        ${pkgs.betad}/bin/betad | ${pkgs.pubmsg}/bin/pubmsg betad
      '';
    };

    # Cross-file additional_sub_topics targeting alphad. Ground truth must
    # show alphad subscribing to "extra-cross-file" via this assignment.
    services.alphad.additional_sub_topics = [ "extra-cross-file" ];
  };
}
