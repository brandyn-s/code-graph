package safegit

import (
	"context"
	"os"
	"os/exec"
)

// safeConfigOverrides neutralizes repo-local git config keys that can execute
// arbitrary commands (core.fsmonitor, core.hooksPath, core.sshCommand,
// core.pager, etc.). Applied via -c flags before any subcommand.
var safeConfigOverrides = []string{
	"-c", "core.fsmonitor=",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.sshCommand=echo",
	"-c", "core.pager=cat",
	"-c", "protocol.file.allow=user",
}

// Command builds an *exec.Cmd for git that is hardened against malicious
// repo-local configuration. It:
//   - sets GIT_CONFIG_NOSYSTEM=1 to ignore /etc/gitconfig
//   - overrides dangerous config keys via -c flags
//   - inherits a minimal environment (PATH only)
//
// The returned Cmd has Dir unset; callers should set cmd.Dir or pass -C as
// part of args if needed.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, len(safeConfigOverrides)+len(args))
	full = append(full, safeConfigOverrides...)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = safeEnv()
	return cmd
}

// safeEnv returns a minimal environment with PATH preserved and
// GIT_CONFIG_NOSYSTEM=1 to prevent system-wide config from injecting
// executable paths.
func safeEnv() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
}
