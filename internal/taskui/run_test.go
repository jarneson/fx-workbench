package taskui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/promptdelivery"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestCommandSidebarActionsReviewAndPullRequest(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	prompt := filepath.Join(directory, "prompt")
	tmux := filepath.Join(directory, "tmux")
	gh := filepath.Join(directory, "gh")
	git := filepath.Join(directory, "git")
	worktree := filepath.Join(directory, "offline-worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
if [[ ${1:-} == load-buffer ]]; then cat > "$PROMPT"; fi
case "${1:-}" in
  display-message)
    if [[ ${*: -1} == '#{session_id}' ]]; then printf '$session\n'; else printf '$session|@planner\n'; fi
    ;;
  new-window) printf '@worker|%%worker\n' ;;
  list-panes) printf '%%agent||review-me|planner\n' ;;
  show-options)
    if [[ ${*: -1} == @fx_state ]]; then printf 'idle\n'
    elif [[ ${*: -1} != @fx_install_state ]]; then printf 'fx-session-offline\n'
    fi
    ;;
  split-window) printf '%%review\n' ;;
esac
`)
	writeTaskUIExecutable(t, gh, `#!/usr/bin/env bash
printf 'gh %s cwd=%s\n' "$*" "$PWD" >> "$CALLS"
`)
	writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$OFFLINE_WORKTREE"
printf 'HEAD abc123\n'
printf 'branch refs/heads/offline-review\n'
`)
	t.Setenv("CALLS", calls)
	t.Setenv("PROMPT", prompt)
	t.Setenv("OFFLINE_WORKTREE", worktree)

	runtime := &sidebarRuntime{}
	actions := &CommandSidebarActions{Runtime: runtime, WorkbenchBinary: "fx-workbench-test", TmuxBinary: tmux, TuicrBinary: "tuicr-test", GHBinary: gh, GitBinary: git, FXCommand: "fx-tmux-test"}
	task := workload.Entity{ID: "review-me", Project: "demo", Branch: "review-me", Repository: directory, SourcePath: "/vault/review-me.md"}
	target := runtimeworkbench.Session{ID: "$session", Windows: []runtimeworkbench.Window{{ID: "@task", Panes: []runtimeworkbench.Pane{{ID: "%agent", Path: directory, TaskID: "review-me", FXSessionID: "fx-session-ready"}}}}}

	if err := actions.Review(context.Background(), task, target); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", "%sidebar")
	offlineTask := workload.Entity{ID: "offline-review", Project: "demo", Branch: "offline-review", Repository: directory, SourcePath: "/vault/offline-review.md", FXSessionID: "fx-session-offline", Title: "Offline review"}
	if err := actions.Review(context.Background(), offlineTask, runtimeworkbench.Session{}); err != nil {
		t.Fatal(err)
	}
	if err := actions.Resume(context.Background(), offlineTask); err != nil {
		t.Fatal(err)
	}
	if err := actions.OpenPullRequest(context.Background(), task, false); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	output := string(content)
	for _, expected := range []string{
		"tmux split-window -b -h -l 60% -P -F #{pane_id} -t %agent -c " + directory + " fx-workbench-test review-relay --target-pane %agent --task-id review-me --tmux " + tmux + " -- tuicr-test --no-update-check",
		"tmux display-message -p -t %sidebar #{session_id}",
		"tmux new-window -d -P -F #{window_id}|#{pane_id} -t $session: -n Offline review -c " + canonicalWorktree + " fx-tmux-test --resume fx-session-offline",
		"tmux set-option -w -t @worker @fx_agent_profile coding",
		"tmux send-keys -l -t %worker /rename Offline review",
		"tmux split-window -b -h -l 60% -P -F #{pane_id} -t %worker -c " + canonicalWorktree + " fx-workbench-test review-relay --target-pane %worker --task-id offline-review",
		"tmux set-option -p -t %review @fx_task_review 1",
		"tmux set-option -p -t %review @fx_task_id review-me",
		"tmux select-pane -t %review -T review",
		"gh pr view review-me --web cwd=" + directory,
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("calls missing %q:\n%s", expected, output)
		}
	}
	promptContent, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"WORKBENCH AGENT PROFILE coding",
		"Resume this task with the latest Workbench guidance.",
		"create it ready for review rather than as a draft",
		"ask the user which reviewer to request",
	} {
		if !strings.Contains(string(promptContent), expected) {
			t.Errorf("prompt missing %q:\n%s", expected, promptContent)
		}
	}
}

func TestCommandSidebarActionsOpenPullRequestReportsPartialSuccess(t *testing.T) {
	t.Run("pull request opens before restore failure", func(t *testing.T) {
		directory := t.TempDir()
		calls := filepath.Join(directory, "calls")
		gh := filepath.Join(directory, "gh")
		writeTaskUIExecutable(t, gh, `#!/usr/bin/env bash
printf 'gh %s\n' "$*" >> "$CALLS"
`)
		t.Setenv("CALLS", calls)

		actions := &CommandSidebarActions{GHBinary: gh}
		task := workload.Entity{ID: "review-me", Branch: "review-me", Repository: directory}
		err := actions.OpenPullRequest(context.Background(), task, true)
		if err == nil || !strings.Contains(err.Error(), "pull request opened; restore agent: task review-me has no fx session to resume") {
			t.Fatalf("error = %v", err)
		}
		content, readErr := os.ReadFile(calls)
		if readErr != nil || !strings.Contains(string(content), "gh pr view review-me --web") {
			t.Fatalf("pull request call = %q, err = %v", content, readErr)
		}
	})

	t.Run("agent restores after pull request failure", func(t *testing.T) {
		directory := t.TempDir()
		calls := filepath.Join(directory, "calls")
		gh := filepath.Join(directory, "gh")
		tmux := filepath.Join(directory, "tmux")
		git := filepath.Join(directory, "git")
		writeTaskUIExecutable(t, gh, `#!/usr/bin/env bash
printf 'gh %s\n' "$*" >> "$CALLS"
printf 'browser failed\n' >&2
exit 1
`)
		writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
case "${1:-}" in
  display-message) printf '$session\n' ;;
  list-panes) printf '@worker|%%worker|0|%s|idle|fx-session|task|review-me|%s|||\n' "$WORKTREE" "$WORKTREE" ;;
esac
`)
		writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$WORKTREE"
printf 'HEAD abc123\n'
printf 'branch refs/heads/review-me\n'
`)
		t.Setenv("CALLS", calls)
		t.Setenv("WORKTREE", directory)
		t.Setenv("TMUX_PANE", "%sidebar")

		runtime := &sidebarRuntime{}
		actions := &CommandSidebarActions{Runtime: runtime, GHBinary: gh, TmuxBinary: tmux, GitBinary: git}
		task := workload.Entity{ID: "review-me", Branch: "review-me", Repository: directory, FXSessionID: "fx-session"}
		err := actions.OpenPullRequest(context.Background(), task, true)
		if err == nil || !strings.Contains(err.Error(), "agent restored; open pull request: browser failed") {
			t.Fatalf("error = %v", err)
		}
		content, readErr := os.ReadFile(calls)
		if readErr != nil {
			t.Fatal(readErr)
		}
		output := string(content)
		if ghCall, tmuxCall := strings.Index(output, "gh pr view review-me --web"), strings.Index(output, "tmux display-message"); ghCall < 0 || tmuxCall < 0 || ghCall > tmuxCall {
			t.Fatalf("calls are out of order:\n%s", output)
		}
		if runtime.focused.ID != "$session" || runtime.focused.Windows[0].Panes[0].ID != "%worker" {
			t.Fatalf("focused = %#v", runtime.focused)
		}
	})
}

func TestCommandSidebarActionsReviewStartsAndReusesOfflineAgent(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	prompt := filepath.Join(directory, "prompt")
	workerMarker := filepath.Join(directory, "worker-started")
	reviewMarker := filepath.Join(directory, "review-started")
	tmux := filepath.Join(directory, "tmux")
	git := filepath.Join(directory, "git")
	worktree := filepath.Join(directory, "offline-worktree")
	packet := filepath.Join(directory, "offline-review.md")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packet, []byte("---\nschema: fx-workbench/v1\ntype: task\nid: offline-review\ntitle: Offline review\nstatus: self-review\nproject: demo\nworkstream: core\nrepository: "+directory+"\nbranch: offline-review\n---\n\n# Offline review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
if [[ ${1:-} == load-buffer ]]; then cat > "$PROMPT"; fi
case "${1:-}" in
  display-message) printf '$session\n' ;;
  list-panes)
    if [[ " $* " == *" -s "* ]]; then
      if [[ -f "$WORKER_MARKER" ]]; then printf '@fresh|%%fresh|0|%s|idle|fx-session-new|task|offline-review|%s|||\n' "$OFFLINE_WORKTREE" "$OFFLINE_WORKTREE"; fi
    else
      printf '%%fresh||offline-review\n'
      if [[ -f "$REVIEW_MARKER" ]]; then printf '%%review|1|offline-review\n'; fi
    fi
    ;;
  new-window) touch "$WORKER_MARKER"; printf '@fresh|%%fresh\n' ;;
  show-options)
    if [[ ${*: -1} == @fx_state ]]; then printf 'idle\n'
    elif [[ ${*: -1} != @fx_install_state ]]; then printf 'fx-session-new\n'
    fi
    ;;
  split-window) touch "$REVIEW_MARKER"; printf '%%review\n' ;;
esac
`)
	writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$OFFLINE_WORKTREE"
printf 'HEAD abc123\n'
printf 'branch refs/heads/offline-review\n'
`)
	t.Setenv("CALLS", calls)
	t.Setenv("PROMPT", prompt)
	t.Setenv("WORKER_MARKER", workerMarker)
	t.Setenv("REVIEW_MARKER", reviewMarker)
	t.Setenv("OFFLINE_WORKTREE", worktree)
	t.Setenv("TMUX_PANE", "%sidebar")

	runtime := &sidebarRuntime{}
	actions := &CommandSidebarActions{Runtime: runtime, Store: &workload.Store{Vault: directory}, WorkbenchBinary: "fx-workbench-test", TmuxBinary: tmux, TuicrBinary: "tuicr-test", GitBinary: git, FXCommand: "fx-tmux-test"}
	task := workload.Entity{Type: "task", ID: "offline-review", Project: "demo", Workstream: "core", Status: "self-review", Branch: "offline-review", Repository: directory, SourcePath: packet, Title: "Offline review"}
	if err := actions.Review(context.Background(), task, runtimeworkbench.Session{}); err != nil {
		t.Fatal(err)
	}
	if err := actions.Review(context.Background(), task, runtimeworkbench.Session{}); err != nil {
		t.Fatal(err)
	}

	callContent, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	output := string(callContent)
	if strings.Count(output, "tmux new-window ") != 1 {
		t.Fatalf("new-window calls = %d, want 1:\n%s", strings.Count(output, "tmux new-window "), output)
	}
	if strings.Count(output, "tmux split-window ") != 1 {
		t.Fatalf("split-window calls = %d, want 1:\n%s", strings.Count(output, "tmux split-window "), output)
	}
	for _, expected := range []string{
		"tmux new-window -d -P -F #{window_id}|#{pane_id} -t $session: -n Offline review -c " + canonicalWorktree + " fx-tmux-test",
		"tmux split-window -b -h -l 60% -P -F #{pane_id} -t %fresh -c " + canonicalWorktree,
		"--target-pane %fresh --task-id offline-review",
		"tmux send-keys -l -t %fresh /rename Offline review",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("calls missing %q:\n%s", expected, output)
		}
	}
	promptContent, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(promptContent), "WORKBENCH AGENT PROFILE coding") {
		t.Fatalf("prompt missing coding profile:\n%s", promptContent)
	}
	if strings.Contains(output, "fx-tmux-test --resume") {
		t.Fatalf("fresh offline agent unexpectedly resumed a session:\n%s", output)
	}
	packetContent, err := os.ReadFile(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packetContent), "status: self-review") || !strings.Contains(string(packetContent), "fx_session_id: fx-session-new") {
		t.Fatalf("session persistence changed review lifecycle:\n%s", packetContent)
	}
}

func TestCommandSidebarActionsReviewAdoptsExactWorktreeAgent(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	tmux := filepath.Join(directory, "tmux")
	git := filepath.Join(directory, "git")
	worktree := filepath.Join(directory, "legacy-worktree")
	worktreeLink := filepath.Join(directory, "legacy-link")
	packet := filepath.Join(directory, "legacy-task.md")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(worktree, worktreeLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packet, []byte("---\nschema: fx-workbench/v1\ntype: task\nid: legacy-task\ntitle: Legacy task\nstatus: self-review\nproject: demo\nworkstream: core\nrepository: "+directory+"\nbranch: legacy-task\n---\n\n# Legacy task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
case "${1:-}" in
  display-message) printf '$session\n' ;;
  list-panes)
    if [[ " $* " == *" -s "* ]]; then
      printf '@dead|%%dead|1|%s|idle|dead-session||||||\n' "$LEGACY_PATH"
      printf '@planner|%%planner|0|%s|idle|planner-session|||||1|\n' "$LEGACY_PATH"
      printf '@sidebar|%%sidebar|0|%s|idle|sidebar-session||||||sidebar\n' "$LEGACY_PATH"
      printf '@review|%%review|0|%s|idle|review-session||||1||\n' "$LEGACY_PATH"
      printf '@shell|%%shell|0|%s||||||||\n' "$LEGACY_PATH"
      printf '@legacy|%%171|0|%s|idle|NerTat1_RtuI||||||\n' "$LEGACY_PATH"
    else
      printf '%%171||legacy-task\n'
    fi
    ;;
  split-window) printf '%%92\n' ;;
esac
`)
	writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$WORKTREE_LINK"
printf 'HEAD abc123\n'
printf 'branch refs/heads/legacy-task\n'
`)
	t.Setenv("CALLS", calls)
	t.Setenv("LEGACY_PATH", worktree)
	t.Setenv("WORKTREE_LINK", worktreeLink)
	t.Setenv("TMUX_PANE", "%sidebar")

	runtime := &sidebarRuntime{}
	actions := &CommandSidebarActions{Runtime: runtime, Store: &workload.Store{Vault: directory}, WorkbenchBinary: "fx-workbench-test", TmuxBinary: tmux, TuicrBinary: "tuicr-test", GitBinary: git, FXCommand: "fx-tmux-test"}
	task := workload.Entity{Type: "task", ID: "legacy-task", Project: "demo", Workstream: "core", Status: "self-review", Branch: "legacy-task", Repository: directory, SourcePath: packet, Title: "Legacy task"}
	legacyTarget := runtimeworkbench.Session{ID: "$session", Windows: []runtimeworkbench.Window{
		{ID: "@planner", Panes: []runtimeworkbench.Pane{{ID: "%sidebar", Path: directory}, {ID: "%planner", Path: directory}}},
		{ID: "@legacy", Panes: []runtimeworkbench.Pane{{ID: "%171", Path: worktree}}},
	}}
	if err := actions.Review(context.Background(), task, legacyTarget); err != nil {
		t.Fatal(err)
	}

	callContent, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	output := string(callContent)
	if strings.Contains(output, "tmux new-window ") {
		t.Fatalf("adoption created a duplicate task window:\n%s", output)
	}
	for _, expected := range []string{
		"tmux set-option -w -t @legacy @fx_task_role task",
		"tmux set-option -p -t %171 @fx_task_id legacy-task",
		"tmux set-option -w -t @legacy @fx_worktree " + canonicalWorktree,
		"tmux set-option -p -t %171 @fx_session_id NerTat1_RtuI",
		"tmux send-keys -l -t %171 /rename Legacy task",
		"tmux paste-buffer -d -p -r",
		"tmux split-window -b -h -l 60% -P -F #{pane_id} -t %171 -c " + canonicalWorktree,
		"tmux select-pane -t %92 -T review",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("calls missing %q:\n%s", expected, output)
		}
	}
	if runtime.focused.ID != "$session" || runtime.focused.Windows[0].ID != "@legacy" || runtime.focused.Windows[0].Panes[0].ID != "%171" {
		t.Fatalf("focused session = %#v, want adopted agent", runtime.focused)
	}
	packetContent, err := os.ReadFile(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packetContent), "status: self-review") || !strings.Contains(string(packetContent), "fx_session_id: NerTat1_RtuI") {
		t.Fatalf("adoption did not preserve status and persist session:\n%s", packetContent)
	}
}

func TestCommandSidebarActionsReviewRejectsAmbiguousWorktreeAgents(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	tmux := filepath.Join(directory, "tmux")
	git := filepath.Join(directory, "git")
	worktree := filepath.Join(directory, "legacy-worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
case "${1:-}" in
  display-message) printf '$session\n' ;;
  list-panes)
    printf '@one|%%171|0|%s|idle|session-one||||||\n' "$WORKTREE"
    printf '@two|%%172|0|%s|working|session-two||||||\n' "$WORKTREE"
    ;;
esac
`)
	writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$WORKTREE"
printf 'HEAD abc123\n'
printf 'branch refs/heads/legacy-task\n'
`)
	t.Setenv("CALLS", calls)
	t.Setenv("WORKTREE", worktree)
	t.Setenv("TMUX_PANE", "%sidebar")

	actions := &CommandSidebarActions{Runtime: &sidebarRuntime{}, Store: &workload.Store{Vault: directory}, TmuxBinary: tmux, GitBinary: git}
	task := workload.Entity{Type: "task", ID: "legacy-task", Project: "demo", Workstream: "core", Status: "self-review", Branch: "legacy-task", Repository: directory, SourcePath: filepath.Join(directory, "legacy-task.md")}
	err := actions.Review(context.Background(), task, runtimeworkbench.Session{})
	if err == nil || !strings.Contains(err.Error(), "multiple untagged fx panes match task worktree") || !strings.Contains(err.Error(), "%171, %172") {
		t.Fatalf("error = %v, want concrete ambiguity", err)
	}
	callContent, readErr := os.ReadFile(calls)
	if readErr != nil {
		t.Fatal(readErr)
	}
	output := string(callContent)
	for _, forbidden := range []string{"tmux new-window ", "tmux set-option ", "tmux split-window "} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("ambiguous adoption performed %q:\n%s", forbidden, output)
		}
	}
}

func TestCommandSidebarActionsResumeRecoversStaleSession(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	launches := filepath.Join(directory, "launches")
	tmux := filepath.Join(directory, "tmux")
	git := filepath.Join(directory, "git")
	worktree := filepath.Join(directory, "task-worktree")
	packet := filepath.Join(directory, "stale-task.md")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packet, []byte("---\nschema: fx-workbench/v1\ntype: task\nid: stale-task\ntitle: Stale task\nstatus: coding\nproject: demo\nworkstream: core\nrepository: "+directory+"\nbranch: stale-task\nfx_session_id: stale-session\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
case "${1:-}" in
  display-message) printf '$session\n' ;;
  list-panes)
    if [[ " $* " == *" -s "* ]]; then exit 0; fi
    if [[ " $* " == *" -t @resume "* ]]; then printf '%%resume|stale-task\n'
    elif [[ " $* " == *" -t %resume "* ]]; then printf '%%resume|1|1|fx-tmux\n'
    fi
    ;;
  new-window)
    count=0
    [[ -f "$LAUNCHES" ]] && count=$(cat "$LAUNCHES")
    count=$((count + 1)); printf '%s' "$count" > "$LAUNCHES"
    if [[ $count == 1 ]]; then printf '@resume|%%resume\n'; else printf '@fresh|%%fresh\n'; fi
    ;;
  show-options)
    if [[ ${*: -1} == @fx_install_state ]]; then printf '\n'
    elif [[ " $* " == *" %resume "* ]]; then
      if [[ ${*: -1} == @fx_state ]]; then printf 'starting\n'; else printf '\n'; fi
    elif [[ ${*: -1} == @fx_state ]]; then printf 'idle\n'
    else printf 'replacement-session\n'
    fi
    ;;
  capture-pane) printf 'fx session stale-session was not found\n' ;;
esac
`)
	writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$WORKTREE"
printf 'HEAD abc123\n'
printf 'branch refs/heads/stale-task\n'
`)
	t.Setenv("CALLS", calls)
	t.Setenv("LAUNCHES", launches)
	t.Setenv("WORKTREE", worktree)
	t.Setenv("TMUX_PANE", "%sidebar")

	runtime := &sidebarRuntime{}
	actions := &CommandSidebarActions{Runtime: runtime, Store: &workload.Store{Vault: directory}, TmuxBinary: tmux, GitBinary: git, FXCommand: "fx-tmux-test"}
	task := workload.Entity{Type: "task", ID: "stale-task", Title: "Stale task", Status: "coding", Project: "demo", Workstream: "core", Repository: directory, Branch: "stale-task", FXSessionID: "stale-session", SourcePath: packet}
	if err := actions.Resume(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	output := string(content)
	if strings.Count(output, "tmux new-window ") != 2 {
		t.Fatalf("new-window calls = %d, want one resume and one replacement:\n%s", strings.Count(output, "tmux new-window "), output)
	}
	for _, expected := range []string{
		"fx-tmux-test --resume stale-session",
		"tmux capture-pane -p -S -200 -t %resume",
		"tmux kill-window -t @resume",
		"tmux new-window -d -P -F #{window_id}|#{pane_id} -t $session: -n Stale task -c " + canonicalWorktree + " fx-tmux-test\n",
		"tmux set-option -p -t %fresh @fx_session_id replacement-session",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("calls missing %q:\n%s", expected, output)
		}
	}
	packetContent, err := os.ReadFile(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packetContent), "status: coding") || !strings.Contains(string(packetContent), "fx_session_id: replacement-session") {
		t.Fatalf("replacement persistence changed lifecycle or missed session:\n%s", packetContent)
	}
	if runtime.focused.Windows[0].ID != "@fresh" || runtime.focused.Windows[0].Panes[0].ID != "%fresh" {
		t.Fatalf("focused session = %#v, want fresh replacement", runtime.focused)
	}
}

func TestCommandSidebarActionsResumeWaitsForTaggedStartupPane(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	polls := filepath.Join(directory, "polls")
	tmux := filepath.Join(directory, "tmux")
	git := filepath.Join(directory, "git")
	worktree := filepath.Join(directory, "task-worktree")
	packet := filepath.Join(directory, "startup-task.md")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packet, []byte("---\nschema: fx-workbench/v1\ntype: task\nid: startup-task\ntitle: Startup task\nstatus: coding\nproject: demo\nworkstream: core\nrepository: "+directory+"\nbranch: startup-task\nfx_session_id: stale-session\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
case "${1:-}" in
  display-message)
    if [[ ${*: -1} == '#{session_id}' ]]; then printf '$session\n'; else printf '0||fx-tmux\n'; fi
    ;;
  list-panes)
    if [[ " $* " == *" -t %startup "* ]]; then printf '%%startup|0||fx-tmux\n'
    else printf '@startup|%%startup|0|%s|starting||task|startup-task|%s|||\n' "$WORKTREE" "$WORKTREE"
    fi
    ;;
  show-options)
    count=0
    [[ -f "$POLLS" ]] && count=$(cat "$POLLS")
    if [[ ${*: -1} == @fx_install_state ]]; then printf '\n'
    elif [[ ${*: -1} == @fx_state ]]; then
      count=$((count + 1)); printf '%s' "$count" > "$POLLS"
      if [[ $count -lt 3 ]]; then printf 'starting\n'; else printf 'idle\n'; fi
    elif [[ $count -lt 3 ]]; then printf '\n'
    else printf 'replacement-session\n'
    fi
    ;;
esac
`)
	writeTaskUIExecutable(t, git, `#!/usr/bin/env bash
printf 'worktree %s\n' "$WORKTREE"
printf 'HEAD abc123\n'
printf 'branch refs/heads/startup-task\n'
`)
	t.Setenv("CALLS", calls)
	t.Setenv("POLLS", polls)
	t.Setenv("WORKTREE", worktree)
	t.Setenv("TMUX_PANE", "%sidebar")

	runtime := &sidebarRuntime{}
	actions := &CommandSidebarActions{Runtime: runtime, Store: &workload.Store{Vault: directory}, TmuxBinary: tmux, GitBinary: git, FXCommand: "fx-tmux-test"}
	task := workload.Entity{Type: "task", ID: "startup-task", Title: "Startup task", Status: "coding", Project: "demo", Workstream: "core", Repository: directory, Branch: "startup-task", FXSessionID: "stale-session", SourcePath: packet}
	if err := actions.Resume(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	output := string(content)
	if strings.Contains(output, "tmux new-window ") {
		t.Fatalf("retry created a duplicate window instead of waiting for tagged pane:\n%s", output)
	}
	if !strings.Contains(output, "tmux set-option -p -t %startup @fx_session_id replacement-session") {
		t.Fatalf("replacement session was not published on reused pane:\n%s", output)
	}
	packetContent, err := os.ReadFile(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packetContent), "status: coding") || !strings.Contains(string(packetContent), "fx_session_id: replacement-session") {
		t.Fatalf("reused startup pane did not persist replacement session:\n%s", packetContent)
	}
}

func TestWaitForTaskAgentReportsExitedPaneOutput(t *testing.T) {
	directory := t.TempDir()
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
case "${1:-}" in
  show-options) exit 1 ;;
  list-panes) printf '%%dead|1|23|fx-tmux\n' ;;
  capture-pane) printf 'resume failed: session missing\n' ;;
esac
`)
	actions := &CommandSidebarActions{TmuxBinary: tmux}
	started := time.Now()
	_, err := actions.waitForTaskAgent(context.Background(), "%dead", "stale-session")
	if err == nil || !isTerminalTaskAgentStartup(err) || !strings.Contains(err.Error(), "status 23") || !strings.Contains(err.Error(), "resume failed: session missing") {
		t.Fatalf("error = %v, want immediate terminal error with process output", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("dead pane detection took %s, want immediate failure", elapsed)
	}
}

func TestWaitForTaskAgentReportsMissingPaneImmediately(t *testing.T) {
	directory := t.TempDir()
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
case "${1:-}" in
  show-options) printf 'no such pane: %%missing\n'; exit 1 ;;
  list-panes) printf "can't find pane: %%missing\n"; exit 1 ;;
esac
`)
	actions := &CommandSidebarActions{TmuxBinary: tmux}
	started := time.Now()
	_, err := actions.waitForTaskAgent(context.Background(), "%missing", "stale-session")
	if err == nil || !isTerminalTaskAgentStartup(err) || !strings.Contains(err.Error(), "pane is missing") || !strings.Contains(err.Error(), "can't find pane: %missing") {
		t.Fatalf("error = %v, want immediate missing-pane diagnostic", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("missing pane detection took %s, want immediate failure", elapsed)
	}
}

func TestSendTaskGuidanceRenamesBeforePrompt(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tmux, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\nif [ \"${1:-}\" = load-buffer ]; then cat >/dev/null; fi\n")
	t.Setenv("CALLS", calls)
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", t.TempDir())
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", t.TempDir())
	actions := &CommandSidebarActions{
		TmuxBinary: tmux,
		Pause: func(_ context.Context, duration time.Duration) error {
			appendTaskUITestCall(t, calls, "pause:"+duration.String())
			return nil
		},
	}
	if err := actions.sendTaskGuidance(context.Background(), workload.Entity{ID: "task", Title: "Build ext4 VHS", Project: "demo", SourcePath: "/task.md"}, "%agent"); err != nil {
		t.Fatal(err)
	}
	output := readTaskUITestFile(t, calls)
	want := []string{"send-keys -l -t %agent /rename Build ext4 VHS", "send-keys -t %agent Enter", "pause:100ms", "load-buffer", "paste-buffer", "pause:" + promptdelivery.SubmitDelay.String(), "send-keys -t %agent Enter"}
	position := -1
	for _, expected := range want {
		next := strings.Index(output[position+1:], expected)
		if next < 0 {
			t.Fatalf("delivery missing %q after byte %d:\n%s", expected, position, output)
		}
		position += next + 1
	}
}

func TestSendTaskGuidanceTreatsAbsentInstallMarkerAsNonBlocking(t *testing.T) {
	directory := t.TempDir()
	prompt := filepath.Join(directory, "prompt")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
case "${1:-}" in
  show-options) printf 'invalid option: @fx_install_state\n' >&2; exit 1 ;;
  load-buffer) cat > "$PROMPT" ;;
esac
`)
	t.Setenv("PROMPT", prompt)
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", t.TempDir())
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", t.TempDir())
	actions := &CommandSidebarActions{TmuxBinary: tmux, Pause: func(context.Context, time.Duration) error { return nil }}
	if err := actions.sendTaskGuidance(context.Background(), workload.Entity{ID: "task", Project: "demo", SourcePath: "/task.md"}, "%agent"); err != nil {
		t.Fatal(err)
	}
	guidance := readTaskUITestFile(t, prompt)
	for _, expected := range []string{"did not start dependency installation", "An absent `@fx_install_state` marker is not a blocker"} {
		if !strings.Contains(guidance, expected) {
			t.Errorf("guidance missing %q:\n%s", expected, guidance)
		}
	}
	for _, forbidden := range []string{"Dependency installation is running concurrently", "wait for `tmux show-options"} {
		if strings.Contains(guidance, forbidden) {
			t.Errorf("guidance contains fresh-launch instruction %q:\n%s", forbidden, guidance)
		}
	}
}

func TestSendTaskGuidanceDoesNotRenameEmptyTitle(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tmux, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\nif [ \"${1:-}\" = load-buffer ]; then cat >/dev/null; fi\n")
	t.Setenv("CALLS", calls)
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", t.TempDir())
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", t.TempDir())
	actions := &CommandSidebarActions{TmuxBinary: tmux, Pause: func(context.Context, time.Duration) error { return nil }}
	if err := actions.sendTaskGuidance(context.Background(), workload.Entity{ID: "task", Project: "demo", SourcePath: "/task.md"}, "%agent"); err != nil {
		t.Fatal(err)
	}
	if output := readTaskUITestFile(t, calls); strings.Contains(output, "/rename") {
		t.Fatalf("empty title triggered rename:\n%s", output)
	}
}

func TestCurrentPaneReviewUsesRelayScope(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
case "${1:-}" in
  display-message)
    case "${*: -1}" in
      '#{@fx_task_id}') printf 'review-me\n' ;;
      '#{window_id}') printf '@window\n' ;;
      '#{pane_current_path}') printf '%s\n' "$WORKTREE" ;;
    esac
    ;;
  split-window) printf '%%review\n' ;;
esac
`)
	t.Setenv("CALLS", calls)
	t.Setenv("WORKTREE", directory)
	t.Setenv("FX_WORKBENCH_TMUX_BIN", tmux)
	t.Setenv("FX_WORKBENCH_BIN", "fx-workbench-test")
	t.Setenv("FX_WORKBENCH_TUICR_BIN", "tuicr-test")
	script := filepath.Join("..", "..", "scripts", "fx-workbench-review-pane")
	command := exec.Command("bash", script, "%agent")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("review pane launcher: %v\n%s", err, output)
	}
	callsOutput := readTaskUITestFile(t, calls)
	want := "fx-workbench-test review-relay --target-pane %agent --task-id review-me --tmux " + tmux + " -- tuicr-test --no-update-check"
	if !strings.Contains(callsOutput, want) || strings.Contains(callsOutput, "tuicr-test --working-tree") {
		t.Fatalf("current-pane relay command does not match sidebar scope boundary:\n%s", callsOutput)
	}
}

func TestTaskReviewScope(t *testing.T) {
	directory := t.TempDir()
	runTaskUIGit(t, directory, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(directory, "task.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTaskUIGit(t, directory, "add", "task.txt")
	runTaskUIGit(t, directory, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	base := strings.TrimSpace(runTaskUIGit(t, directory, "rev-parse", "HEAD"))

	if _, err := taskReviewScope(context.Background(), directory, "git"); err == nil || !strings.Contains(err.Error(), "no committed or working-tree changes") {
		t.Fatalf("clean main error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "task.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if scope, err := taskReviewScope(context.Background(), directory, "git"); err != nil || strings.Join(scope, " ") != "--working-tree" {
		t.Fatalf("working-tree scope = %v, %v", scope, err)
	}
	runTaskUIGit(t, directory, "restore", "task.txt")
	runTaskUIGit(t, directory, "switch", "-c", "task")
	if err := os.WriteFile(filepath.Join(directory, "task.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for index := 2; index <= 8; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("task-%d.txt", index)), []byte("committed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runTaskUIGit(t, directory, "add", ".")
	runTaskUIGit(t, directory, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task")
	if changed := strings.Fields(runTaskUIGit(t, directory, "diff", "--name-only", base+"..HEAD")); len(changed) != 8 {
		t.Fatalf("committed files = %v, want 8", changed)
	}
	want := "--revisions " + base + "..HEAD --working-tree"
	if scope, err := taskReviewScope(context.Background(), directory, "git"); err != nil || strings.Join(scope, " ") != want {
		t.Fatalf("clean branch scope = %v, %v; want %q", scope, err, want)
	}
	if err := os.WriteFile(filepath.Join(directory, "task.txt"), []byte("committed and dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if scope, err := taskReviewScope(context.Background(), directory, "git"); err != nil || strings.Join(scope, " ") != want {
		t.Fatalf("committed and dirty scope = %v, %v; want %q", scope, err, want)
	}
}

func TestRunReviewRelayExportsToExactAgentPane(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	prompt := filepath.Join(directory, "prompt")
	tuicr := filepath.Join(directory, "tuicr")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tuicr, `#!/usr/bin/env bash
printf 'tuicr %s\n' "$*" >> "$CALLS"
printf '## Review\n\n1. `+"`src/main.go:12`"+` - Handle the error.\n'
`)
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
if [[ ${1:-} == load-buffer ]]; then cat > "$PROMPT"; fi
`)
	t.Setenv("CALLS", calls)
	t.Setenv("PROMPT", prompt)
	setTaskUIReviewGit(t, directory)

	if err := runReviewRelay([]string{"--target-pane", "%agent", "--task-id", "review-me", "--tmux", tmux, "--", tuicr}, io.Discard, func(_ context.Context, duration time.Duration) error {
		appendTaskUITestCall(t, calls, "pause:"+duration.String())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	callContent, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"tuicr --revisions base123..HEAD --working-tree --stdout", "tmux paste-buffer -d -p -r", "-t %agent", "tmux send-keys -t %agent Enter"} {
		if !strings.Contains(string(callContent), expected) {
			t.Errorf("calls missing %q:\n%s", expected, callContent)
		}
	}
	paste := strings.Index(string(callContent), "tmux paste-buffer")
	pause := strings.Index(string(callContent), "pause:"+promptdelivery.SubmitDelay.String())
	submit := strings.Index(string(callContent), "tmux send-keys")
	if paste < 0 || pause < paste || submit < pause {
		t.Fatalf("review delivery order is wrong:\n%s", callContent)
	}
	promptContent, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Self-review feedback for task review-me", "src/main.go:12", "Handle the error", "create it ready for review rather than as a draft", "ask the user which reviewer to request"} {
		if !strings.Contains(string(promptContent), expected) {
			t.Errorf("prompt missing %q:\n%s", expected, promptContent)
		}
	}
}

func TestRunReviewRelaySendsCompletedEmptyReview(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	prompt := filepath.Join(directory, "prompt")
	tuicr := filepath.Join(directory, "tuicr")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tuicr, "#!/bin/sh\nprintf 'tuicr-summary: reviewed 3/3 files, 0 comments added\\n' >&2\n")
	writeTaskUIExecutable(t, tmux, `#!/usr/bin/env bash
printf 'tmux %s\n' "$*" >> "$CALLS"
if [[ ${1:-} == load-buffer ]]; then cat > "$PROMPT"; fi
`)
	t.Setenv("CALLS", calls)
	t.Setenv("PROMPT", prompt)
	setTaskUIReviewGit(t, directory)

	if err := RunReviewRelay([]string{"--target-pane", "%agent", "--task-id", "review-me", "--tmux", tmux, "--", tuicr}, io.Discard); err != nil {
		t.Fatal(err)
	}
	promptContent, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"completed for task review-me with no comments", "All 3 changed files were marked reviewed", "commit the completed work", "push the task branch", "open a ready-for-review pull request rather than a draft", "After PR creation succeeds, immediately run `fx-workbench status --set in-review review-me`", "ask the user which reviewer to request"} {
		if !strings.Contains(string(promptContent), expected) {
			t.Errorf("prompt missing %q:\n%s", expected, promptContent)
		}
	}
}

func TestRunReviewRelayDoesNotSendIncompleteEmptyReview(t *testing.T) {
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	tuicr := filepath.Join(directory, "tuicr")
	tmux := filepath.Join(directory, "tmux")
	writeTaskUIExecutable(t, tuicr, "#!/bin/sh\nprintf 'tuicr-summary: reviewed 1/3 files, 0 comments added\\n' >&2\n")
	writeTaskUIExecutable(t, tmux, "#!/bin/sh\nprintf 'tmux %s\\n' \"$*\" >> \"$CALLS\"\n")
	t.Setenv("CALLS", calls)
	setTaskUIReviewGit(t, directory)

	if err := RunReviewRelay([]string{"--target-pane", "%agent", "--task-id", "review-me", "--tmux", tmux, "--", tuicr}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(calls); err == nil && len(content) > 0 {
		t.Fatalf("unexpected tmux delivery:\n%s", content)
	}
}

func runTaskUIGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func setTaskUIReviewGit(t *testing.T, directory string) {
	t.Helper()
	git := filepath.Join(directory, "git-review-scope")
	writeTaskUIExecutable(t, git, `#!/bin/sh
case "$1" in
  merge-base) printf 'base123\n' ;;
  status) ;;
  rev-parse) printf 'head456\n' ;;
esac
`)
	t.Setenv("FX_WORKBENCH_GIT_BIN", git)
}

func appendTaskUITestCall(t *testing.T, path, value string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(value + "\n"); err != nil {
		t.Fatal(err)
	}
}

func readTaskUITestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func writeTaskUIExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
