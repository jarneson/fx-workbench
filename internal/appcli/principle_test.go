package appcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/principles"
	"github.com/jarneson/fx-workbench/internal/promptdelivery"
)

func TestPrincipleCLIListsShowsAndRenders(t *testing.T) {
	root := t.TempDir()
	writePrincipleFixture(t, root, "alpha", "Alpha", "First rule.", "# Alpha\n\nDo alpha.\n")
	writePrincipleFixture(t, root, "beta", "Beta", "Second rule.", "# Beta\n\nDo beta.\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"principle", "list", "--root", root, "--format", "tsv"}, &stdout, &stderr)
	if code != 0 || stdout.String() != "alpha\tAlpha\tFirst rule.\nbeta\tBeta\tSecond rule.\n" {
		t.Fatalf("list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"principle", "show", "--root", root, "alpha"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Do alpha") {
		t.Fatalf("show code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"principle", "render", "--root", root, "beta", "alpha"}, &stdout, &stderr)
	if code != 0 || strings.Index(stdout.String(), "Do beta") > strings.Index(stdout.String(), "Do alpha") {
		t.Fatalf("render code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPrincipleApplyUsesOneLiteralTmuxDelivery(t *testing.T) {
	root := t.TempDir()
	writePrincipleFixture(t, root, "alpha", "Alpha", "First rule.", "# Alpha\n\nDo alpha.\n")
	calls := filepath.Join(t.TempDir(), "calls")
	tmux := filepath.Join(t.TempDir(), "tmux")
	writeExecutable(t, tmux, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >> "$CALLS"
case "$1" in
  display-message) printf '0\n' ;;
  show-options) printf 'fx-session-1\n' ;;
  load-buffer) cat >/dev/null ;;
esac
`)
	t.Setenv("CALLS", calls)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"principle", "apply", "--root", root, "--tmux-bin", tmux, "--target-pane", "%9", "alpha"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	logged := readOptionalFile(t, calls)
	for _, command := range []string{"display-message", "show-options", "load-buffer", "paste-buffer", "send-keys"} {
		if strings.Count(logged, command) != 1 {
			t.Errorf("%s count in %q", command, logged)
		}
	}
	if !strings.Contains(stdout.String(), "applied: alpha") || !strings.Contains(stdout.String(), "target pane: %9") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestApplyPrinciplesPausesBeforeSubmit(t *testing.T) {
	root := t.TempDir()
	writePrincipleFixture(t, root, "alpha", "Alpha", "First rule.", "body\n")
	bundle, err := principles.New(root).Render([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(t.TempDir(), "calls")
	tmux := filepath.Join(t.TempDir(), "tmux")
	writeExecutable(t, tmux, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >> "$CALLS"
case "$1" in
  display-message) printf '0\n' ;;
  show-options) printf 'fx-session-1\n' ;;
  load-buffer) cat >/dev/null ;;
esac
`)
	t.Setenv("CALLS", calls)
	pause := func(_ context.Context, duration time.Duration) error {
		file, openErr := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if openErr != nil {
			return openErr
		}
		defer file.Close()
		_, writeErr := file.WriteString("pause:" + duration.String() + "\n")
		return writeErr
	}
	if err := applyPrinciples(context.Background(), tmux, "%9", bundle, pause); err != nil {
		t.Fatal(err)
	}
	logged := strings.Fields(readOptionalFile(t, calls))
	want := []string{"display-message", "show-options", "load-buffer", "paste-buffer", "pause:" + promptdelivery.SubmitDelay.String(), "send-keys"}
	if strings.Join(logged, ",") != strings.Join(want, ",") {
		t.Fatalf("delivery order = %v, want %v", logged, want)
	}
}

func TestApplyPrinciplesDoesNotSubmitWhenPauseFails(t *testing.T) {
	root := t.TempDir()
	writePrincipleFixture(t, root, "alpha", "Alpha", "First rule.", "body\n")
	bundle, err := principles.New(root).Render([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(t.TempDir(), "calls")
	tmux := filepath.Join(t.TempDir(), "tmux")
	writeExecutable(t, tmux, "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$CALLS\"\ncase \"$1\" in display-message) printf '0\\n' ;; show-options) printf 'fx-session-1\\n' ;; load-buffer) cat >/dev/null ;; esac\n")
	t.Setenv("CALLS", calls)
	pauseErr := errors.New("pause failed")
	err = applyPrinciples(context.Background(), tmux, "%9", bundle, func(context.Context, time.Duration) error { return pauseErr })
	if !errors.Is(err, pauseErr) {
		t.Fatalf("error = %v, want %v", err, pauseErr)
	}
	if strings.Contains(readOptionalFile(t, calls), "send-keys") {
		t.Fatalf("principle submitted after pause failure: %s", readOptionalFile(t, calls))
	}
}

func TestPrincipleApplyRejectsUnidentifiedPaneBeforeDelivery(t *testing.T) {
	root := t.TempDir()
	writePrincipleFixture(t, root, "alpha", "Alpha", "First rule.", "body\n")
	calls := filepath.Join(t.TempDir(), "calls")
	tmux := filepath.Join(t.TempDir(), "tmux")
	writeExecutable(t, tmux, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >> "$CALLS"
case "$1" in
  display-message) printf '0\n' ;;
  show-options) exit 1 ;;
esac
`)
	t.Setenv("CALLS", calls)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"principle", "apply", "--root", root, "--tmux-bin", tmux, "--target-pane", "%9", "alpha"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "not an identified fx pane") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	logged := readOptionalFile(t, calls)
	if strings.Contains(logged, "load-buffer") {
		t.Fatalf("invalid pane received delivery: %q", logged)
	}
}

func TestPrinciplePickSupportsMultiSelectAndCancel(t *testing.T) {
	root := t.TempDir()
	writePrincipleFixture(t, root, "alpha", "Alpha", "First rule.", "body alpha\n")
	writePrincipleFixture(t, root, "beta", "Beta", "Second rule.", "body beta\n")
	tmuxCalls := filepath.Join(t.TempDir(), "tmux-calls")
	tmux := filepath.Join(t.TempDir(), "tmux")
	writeExecutable(t, tmux, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >> "$CALLS"
case "$1" in
  display-message) printf '0\n' ;;
  show-options) printf 'fx-session-1\n' ;;
  load-buffer) cat >/dev/null ;;
esac
`)
	fzf := filepath.Join(t.TempDir(), "fzf")
	fzfArgs := filepath.Join(t.TempDir(), "fzf-args")
	writeExecutable(t, fzf, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" > "$FZF_ARGS"
cat | head -2
`)
	t.Setenv("CALLS", tmuxCalls)
	t.Setenv("FZF_ARGS", fzfArgs)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"principle", "pick", "--root", root, "--tmux-bin", tmux, "--fzf-bin", fzf, "--target-pane", "%4"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "applied: alpha, beta") {
		t.Fatalf("pick code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	arguments := readOptionalFile(t, fzfArgs)
	for _, expected := range []string{"--multi", "--preview", "principle show", "{1}"} {
		if !strings.Contains(arguments, expected) {
			t.Errorf("fzf args missing %q: %s", expected, arguments)
		}
	}

	cancel := filepath.Join(t.TempDir(), "fzf-cancel")
	writeExecutable(t, cancel, "#!/usr/bin/env bash\nexit 130\n")
	before := readOptionalFile(t, tmuxCalls)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"principle", "pick", "--root", root, "--tmux-bin", tmux, "--fzf-bin", cancel, "--target-pane", "%4"}, &stdout, &stderr)
	if code != 0 || readOptionalFile(t, tmuxCalls) != before {
		t.Fatalf("cancel code=%d calls=%q before=%q stderr=%q", code, readOptionalFile(t, tmuxCalls), before, stderr.String())
	}
}

func writePrincipleFixture(t *testing.T, root, id, title, description, body string) {
	t.Helper()
	content := "---\nid: " + id + "\ntitle: " + title + "\ndescription: " + description + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(root, id+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
