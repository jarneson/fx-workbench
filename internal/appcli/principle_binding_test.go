package appcli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrinciplePickerBindingExpandsInvokingPaneBeforePopup(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "config", "tmux.conf"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	start := strings.Index(text, "bind-key I ")
	if start < 0 {
		t.Fatal("principle picker binding is missing")
	}
	line := strings.SplitN(text[start:], "\n", 2)[0]
	for _, expected := range []string{
		"bind-key I run-shell -b",
		"display-popup",
		"-t '#{pane_id}'",
		"FX_PRINCIPLE_TARGET_PANE='#{pane_id}'",
		"fx-principle pick",
	} {
		if !strings.Contains(line, expected) {
			t.Errorf("binding missing %q: %s", expected, line)
		}
	}
}
