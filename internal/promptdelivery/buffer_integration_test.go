package promptdelivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPasteStreams64KiBThroughTmux(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	directory := t.TempDir()
	socket := fmt.Sprintf("fx-prompt-%d-%d", os.Getpid(), time.Now().UnixNano())
	output := filepath.Join(directory, "prompt")
	runTmux := func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		arguments := append([]string{"-L", socket, "-f", "/dev/null"}, args...)
		command := exec.CommandContext(ctx, tmux, arguments...)
		command.Stdin = stdin
		return command.CombinedOutput()
	}
	if commandOutput, commandErr := runTmux(context.Background(), nil, "new-session", "-d", "-s", "prompt", "cat > "+shellQuote(output)); commandErr != nil {
		t.Fatalf("start tmux: %v\n%s", commandErr, commandOutput)
	}
	t.Cleanup(func() { _, _ = runTmux(context.Background(), nil, "kill-server") })

	var payload bytes.Buffer
	for payload.Len() < 64*1024 {
		payload.WriteString("  quotes '\"` and UTF-8 λ日 ")
		payload.WriteString(strings.Repeat("x", 980))
		payload.WriteByte('\n')
	}
	payload.WriteString("trailing spaces  ")

	if err := Paste(context.Background(), runTmux, "prompt", "prompt:0.0", payload.Bytes()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(SubmitDelay)
	if commandOutput, commandErr := runTmux(context.Background(), nil, "send-keys", "-t", "prompt:0.0", "Enter", "C-d"); commandErr != nil {
		t.Fatalf("submit prompt: %v\n%s", commandErr, commandOutput)
	}

	want := append(append([]byte(nil), payload.Bytes()...), '\n')
	for range 100 {
		got, readErr := os.ReadFile(output)
		if readErr == nil && bytes.Equal(got, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Fatalf("pasted %d bytes, want %d exact bytes", len(got), len(want))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
