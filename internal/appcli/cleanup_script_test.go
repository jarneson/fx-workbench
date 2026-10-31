package appcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCleanupScriptDeletesSelectedBranchWithoutValidation(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required to exercise the cleanup adapter")
	}

	dir := t.TempDir()
	callsPath := filepath.Join(dir, "calls")
	fzfInputPath := filepath.Join(dir, "fzf-input")
	gitPath := filepath.Join(dir, "git")
	workbenchPath := filepath.Join(dir, "fx-workbench")
	fzfPath := filepath.Join(dir, "fzf")
	tmuxPath := filepath.Join(dir, "tmux")

	writeExecutable(t, gitPath, `#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >> "$CALLS_PATH"
if [[ $* == "worktree list --porcelain" ]]; then
  cat <<'EOF'
worktree /repo/demo
HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
branch refs/heads/main

worktree /worktrees/ship-it
HEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
branch refs/heads/ship-it

EOF
fi
`)
	writeExecutable(t, workbenchPath, `#!/usr/bin/env bash
set -euo pipefail
printf 'workbench %s\n' "$*" >> "$CALLS_PATH"
cat <<'EOF'
[{"id":"ship-task","status":"done","branch":"ship-it","repository":"/repo/demo"}]
EOF
`)
	writeExecutable(t, fzfPath, `#!/usr/bin/env bash
set -euo pipefail
cat > "$FZF_INPUT_PATH"
cat "$FZF_INPUT_PATH"
`)
	writeExecutable(t, tmuxPath, `#!/usr/bin/env bash
set -euo pipefail
printf 'tmux %s\n' "$*" >> "$CALLS_PATH"
if [[ ${1:-} == list-sessions ]]; then
  printf '$7\tship-it\t/worktrees/ship-it\n'
fi
`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", cleanupScriptPath(t))
	cmd.Env = append(os.Environ(),
		"TMUX=",
		"FX_WORKBENCH_GIT_BIN="+gitPath,
		"FX_WORKBENCH_BIN="+workbenchPath,
		"FX_WORKBENCH_FZF_BIN="+fzfPath,
		"FX_WORKBENCH_JQ_BIN="+jq,
		"FX_WORKBENCH_TMUX_BIN="+tmuxPath,
		"CALLS_PATH="+callsPath,
		"FZF_INPUT_PATH="+fzfInputPath,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("cleanup timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("cleanup failed: %v\n%s", err, output)
	}

	if got := readOptionalFile(t, fzfInputPath); got != "done\tship-task\tship-it\t/worktrees/ship-it\n" {
		t.Fatalf("unexpected selector row: %q", got)
	}
	calls := strings.Join(readLines(t, callsPath), "\n")
	for _, expected := range []string{
		"git worktree list --porcelain",
		"workbench list --type task --all --json",
		"git -C /repo/demo worktree remove /worktrees/ship-it --force",
		"git -C /repo/demo branch -D -- ship-it",
		"tmux list-sessions -F",
		"tmux kill-session -t $7",
	} {
		if !strings.Contains(calls, expected) {
			t.Errorf("calls missing %q:\n%s", expected, calls)
		}
	}
	for _, forbidden := range []string{"cleanup", "status", "merge-base", "cherry", "rev-parse"} {
		if strings.Contains(calls, forbidden) {
			t.Errorf("calls contain forbidden validation %q:\n%s", forbidden, calls)
		}
	}
}

func TestCleanupScriptQueuesDeletionInsideTmux(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required to exercise the cleanup adapter")
	}

	dir := t.TempDir()
	callsPath := filepath.Join(dir, "calls")
	gitPath := filepath.Join(dir, "git")
	workbenchPath := filepath.Join(dir, "fx-workbench")
	fzfPath := filepath.Join(dir, "fzf")
	tmuxPath := filepath.Join(dir, "tmux")

	writeExecutable(t, gitPath, `#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >> "$CALLS_PATH"
if [[ $* == "worktree list --porcelain" ]]; then
  cat <<'EOF'
worktree /repo/demo
branch refs/heads/main

worktree /worktrees/ship-it
branch refs/heads/ship-it

EOF
fi
`)
	writeExecutable(t, workbenchPath, `#!/usr/bin/env bash
printf '[]\n'
`)
	writeExecutable(t, fzfPath, `#!/usr/bin/env bash
cat
`)
	writeExecutable(t, tmuxPath, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS_PATH"
`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", cleanupScriptPath(t))
	cmd.Env = append(os.Environ(),
		"TMUX=/tmp/tmux,1,0",
		"FX_WORKBENCH_GIT_BIN="+gitPath,
		"FX_WORKBENCH_BIN="+workbenchPath,
		"FX_WORKBENCH_FZF_BIN="+fzfPath,
		"FX_WORKBENCH_JQ_BIN="+jq,
		"FX_WORKBENCH_TMUX_BIN="+tmuxPath,
		"CALLS_PATH="+callsPath,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("cleanup timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("cleanup failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Deletion queued in the background.") {
		t.Fatalf("missing queued message: %s", output)
	}

	calls := strings.Join(readLines(t, callsPath), "\n")
	for _, expected := range []string{"git worktree list --porcelain", "tmux run-shell -b", "--delete", "/repo/demo", "ship-it", "/worktrees/ship-it"} {
		if !strings.Contains(calls, expected) {
			t.Errorf("calls missing %q:\n%s", expected, calls)
		}
	}
	for _, forbidden := range []string{"git -C", "tmux list-sessions", "tmux kill-session"} {
		if strings.Contains(calls, forbidden) {
			t.Errorf("popup executed slow cleanup inline via %q:\n%s", forbidden, calls)
		}
	}
}

func cleanupScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "fx-workbench-cleanup"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("locate cleanup script: %v", err)
	}
	return path
}
