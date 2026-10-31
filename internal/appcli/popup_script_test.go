package appcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewWorkstreamPopup(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required to exercise the popup adapter")
	}

	script := newWorkstreamPopupPath(t)

	t.Run("single registered repository excludes unregistered workspace", func(t *testing.T) {
		result := runNewWorkstreamPopup(t, script, jq, popupRun{
			resolution: `{"workspace_root":"/tmp/demo-worktree","project":{"id":"demo","root_repository":"/repo/demo","repositories":["/repo/demo"]}}`,
			input:      "Ship It\n\n",
		})
		if result.err != nil {
			t.Fatalf("popup failed: %v\n%s", result.err, result.output)
		}
		assertCalls(t, result.calls,
			"project resolve --json",
			"scaffold workstream --project demo --id ship-it --title Ship It --repository /repo/demo --status active",
			"plan --project demo --workstream ship-it",
		)
		if result.fzfInput != "" {
			t.Fatalf("single repository unexpectedly opened fzf with %q", result.fzfInput)
		}
	})

	t.Run("multiple repositories use selected registered repository", func(t *testing.T) {
		result := runNewWorkstreamPopup(t, script, jq, popupRun{
			resolution:   `{"workspace_root":"/repo/two","project":{"id":"demo","root_repository":"/repo/root","repositories":["/repo/one","/repo/two"]}}`,
			fzfSelection: "/repo/one",
			input:        "Core Runtime\ncustom-id\n",
		})
		if result.err != nil {
			t.Fatalf("popup failed: %v\n%s", result.err, result.output)
		}
		assertCalls(t, result.calls,
			"project resolve --json",
			"scaffold workstream --project demo --id custom-id --title Core Runtime --repository /repo/one --status active",
			"plan --project demo --workstream custom-id",
		)
		if result.fzfInput != "/repo/two\n/repo/one\n/repo/root\n" {
			t.Fatalf("unexpected repository choices/order: %q", result.fzfInput)
		}
	})

	for _, test := range []struct {
		name          string
		input         string
		fzfCancel     bool
		resolution    string
		expectedCalls []string
	}{
		{
			name:          "repository cancellation",
			fzfCancel:     true,
			resolution:    `{"workspace_root":"/repo/one","project":{"id":"demo","root_repository":"/repo/root","repositories":["/repo/one"]}}`,
			expectedCalls: []string{"project resolve --json"},
		},
		{
			name:          "title cancellation",
			input:         "\x1b",
			resolution:    singleRepositoryResolution,
			expectedCalls: []string{"project resolve --json"},
		},
		{
			name:          "id cancellation",
			input:         "Ship It\n\x1b",
			resolution:    singleRepositoryResolution,
			expectedCalls: []string{"project resolve --json"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runNewWorkstreamPopup(t, script, jq, popupRun{
				resolution: test.resolution,
				input:      test.input,
				fzfCancel:  test.fzfCancel,
			})
			if result.err != nil {
				t.Fatalf("cancellation returned an error: %v\n%s", result.err, result.output)
			}
			assertCalls(t, result.calls, test.expectedCalls...)
		})
	}

	t.Run("scaffold failure does not open planner", func(t *testing.T) {
		result := runNewWorkstreamPopup(t, script, jq, popupRun{
			resolution:  singleRepositoryResolution,
			input:       "Ship It\n\n\n",
			failCommand: "scaffold",
		})
		assertExitCode(t, result.err, 1)
		assertCalls(t, result.calls,
			"project resolve --json",
			"scaffold workstream --project demo --id ship-it --title Ship It --repository /repo/demo --status active",
		)
		if !strings.Contains(result.output, "scaffold failed") {
			t.Fatalf("missing scaffold error in output: %s", result.output)
		}
	})

	t.Run("planner failure remains actionable", func(t *testing.T) {
		result := runNewWorkstreamPopup(t, script, jq, popupRun{
			resolution:  singleRepositoryResolution,
			input:       "Ship It\n\n\n",
			failCommand: "plan",
		})
		assertExitCode(t, result.err, 1)
		assertCalls(t, result.calls,
			"project resolve --json",
			"scaffold workstream --project demo --id ship-it --title Ship It --repository /repo/demo --status active",
			"plan --project demo --workstream ship-it",
		)
		if !strings.Contains(result.output, "planner failed") {
			t.Fatalf("missing planner error in output: %s", result.output)
		}
	})
}

const singleRepositoryResolution = `{"workspace_root":"/tmp/demo-worktree","project":{"id":"demo","root_repository":"/repo/demo","repositories":["/repo/demo"]}}`

type popupRun struct {
	resolution   string
	input        string
	fzfSelection string
	fzfCancel    bool
	failCommand  string
}

type popupResult struct {
	calls    []string
	fzfInput string
	output   string
	err      error
}

func runNewWorkstreamPopup(t *testing.T, script, jq string, run popupRun) popupResult {
	t.Helper()
	dir := t.TempDir()
	callsPath := filepath.Join(dir, "calls")
	resolutionPath := filepath.Join(dir, "resolution.json")
	fzfInputPath := filepath.Join(dir, "fzf-input")
	workbenchPath := filepath.Join(dir, "fx-workbench")
	fzfPath := filepath.Join(dir, "fzf")

	if err := os.WriteFile(resolutionPath, []byte(run.resolution), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, workbenchPath, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CALLS_PATH"
case "${1:-}" in
  project)
    cat "$RESOLUTION_PATH"
    ;;
  scaffold)
    if [[ ${FAIL_COMMAND:-} == scaffold ]]; then
      printf 'scaffold failed\n' >&2
      exit 42
    fi
    printf '/tmp/workstream.md\n'
    ;;
  plan)
    if [[ ${FAIL_COMMAND:-} == plan ]]; then
      printf 'planner failed\n' >&2
      exit 43
    fi
    printf 'planner opened\n'
    ;;
esac
`)
	writeExecutable(t, fzfPath, `#!/usr/bin/env bash
set -euo pipefail
cat > "$FZF_INPUT_PATH"
[[ ${FZF_CANCEL:-0} != 1 ]] || exit 1
printf '%s\n' "$FZF_SELECTION"
`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Stdin = strings.NewReader(run.input)
	cmd.Env = append(os.Environ(),
		"FX_WORKBENCH_BIN="+workbenchPath,
		"FX_WORKBENCH_FZF_BIN="+fzfPath,
		"FX_WORKBENCH_JQ_BIN="+jq,
		"CALLS_PATH="+callsPath,
		"RESOLUTION_PATH="+resolutionPath,
		"FZF_INPUT_PATH="+fzfInputPath,
		"FZF_SELECTION="+run.fzfSelection,
		"FAIL_COMMAND="+run.failCommand,
	)
	if run.fzfCancel {
		cmd.Env = append(cmd.Env, "FZF_CANCEL=1")
	}
	output, cmdErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("popup timed out: %v\n%s", ctx.Err(), output)
	}

	return popupResult{
		calls:    readLines(t, callsPath),
		fzfInput: readOptionalFile(t, fzfInputPath),
		output:   string(output),
		err:      cmdErr,
	}
}

func newWorkstreamPopupPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "fx-workbench-new-workstream"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("locate popup script: %v", err)
	}
	return path
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(content)), "\n")
}

func readOptionalFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func assertCalls(t *testing.T, actual []string, expected ...string) {
	t.Helper()
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected calls:\nactual:   %q\nexpected: %q", actual, expected)
	}
}

func assertExitCode(t *testing.T, err error, expected int) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != expected {
		t.Fatalf("exit error = %v, want code %d", err, expected)
	}
}
