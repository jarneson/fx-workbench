package promptdelivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type TmuxRunner func(context.Context, io.Reader, ...string) ([]byte, error)

func Exec(binary string) TmuxRunner {
	return func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, binary, args...)
		command.Stdin = stdin
		return command.CombinedOutput()
	}
}

func Paste(ctx context.Context, run TmuxRunner, buffer, pane string, payload []byte) error {
	if output, err := run(ctx, bytes.NewReader(payload), "load-buffer", "-b", buffer, "-"); err != nil {
		cleanupBuffer(run, buffer)
		return fmt.Errorf("tmux load-buffer: %s", commandError(output, err))
	}
	if output, err := run(ctx, nil, "paste-buffer", "-d", "-p", "-r", "-b", buffer, "-t", pane); err != nil {
		cleanupBuffer(run, buffer)
		return fmt.Errorf("tmux paste-buffer: %s", commandError(output, err))
	}
	return nil
}

func cleanupBuffer(run TmuxRunner, buffer string) {
	_, _ = run(context.Background(), nil, "delete-buffer", "-b", buffer)
}

func commandError(output []byte, err error) string {
	if message := strings.TrimSpace(string(output)); message != "" {
		return message
	}
	return err.Error()
}
