package agentcli

import (
	"path/filepath"
	"strconv"
)

func CodexArgs(project, state, model string, hooks []string) []string {
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"--ignore-user-config", "--ignore-rules", "--dangerously-bypass-hook-trust",
		"--disable", "plugins", "--disable", "remote_plugin", "--disable", "apps", "--disable", "shell_snapshot",
		"-m", model, "-c", `model_reasoning_effort="low"`,
		"-s", "workspace-write",
		"-c", "sandbox_workspace_write.network_access=false",
		"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
		"-c", `approval_policy="never"`,
		"-c", "sqlite_home=" + strconv.Quote(state),
		"-c", "log_dir=" + strconv.Quote(filepath.Join(state, "log")),
		"-C", project}
	return append(args, hooks...)
}
