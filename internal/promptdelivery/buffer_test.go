package promptdelivery

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestPasteStreamsExactBytes(t *testing.T) {
	payload := []byte("  first line\nquotes: '\"`\nUTF-8: λ日\nlast line  ")
	var received []byte
	var commands []string
	run := func(_ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		commands = append(commands, args[0])
		if stdin != nil {
			var err error
			received, err = io.ReadAll(stdin)
			if err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if err := Paste(context.Background(), run, "prompt", "%7", payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, payload) {
		t.Fatalf("payload = %q, want %q", received, payload)
	}
	if want := []string{"load-buffer", "paste-buffer"}; !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestPasteDeletesBufferAfterLoadOrPasteFailure(t *testing.T) {
	for _, failure := range []struct {
		command string
		want    []string
	}{
		{command: "load-buffer", want: []string{"load-buffer", "delete-buffer"}},
		{command: "paste-buffer", want: []string{"load-buffer", "paste-buffer", "delete-buffer"}},
	} {
		t.Run(failure.command, func(t *testing.T) {
			commandErr := errors.New(failure.command + " failed")
			var commands []string
			run := func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
				commands = append(commands, args[0])
				if args[0] == failure.command {
					return nil, commandErr
				}
				return nil, nil
			}
			err := Paste(context.Background(), run, "prompt", "%7", []byte("payload"))
			if err == nil || !strings.Contains(err.Error(), commandErr.Error()) {
				t.Fatalf("error = %v, want it to contain %v", err, commandErr)
			}
			if !reflect.DeepEqual(commands, failure.want) {
				t.Fatalf("commands = %v, want %v", commands, failure.want)
			}
		})
	}
}
