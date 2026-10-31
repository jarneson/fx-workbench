package mainsync

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Synchronizer advances the worktree holding local main to exactly origin/main.
type Synchronizer interface {
	Sync(context.Context, string) (Result, error)
}

type Result struct {
	MainWorktree string
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	return command.CombinedOutput()
}

type Service struct {
	runner commandRunner
}

func New() *Service {
	return &Service{runner: execRunner{}}
}

func (service *Service) Sync(ctx context.Context, repository string) (Result, error) {
	if repository == "" {
		return Result{}, errors.New("repository is required to synchronize main")
	}
	rootOutput, err := service.runner.Run(ctx, repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return Result{}, fmt.Errorf("resolve Git repository from %s: %s", repository, commandError(rootOutput, err))
	}
	root := strings.TrimSpace(string(rootOutput))
	worktrees, err := service.runner.Run(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return Result{}, fmt.Errorf("list worktrees for %s: %s", root, commandError(worktrees, err))
	}
	mainWorktree := mainWorktreePath(string(worktrees))
	if mainWorktree == "" {
		return Result{}, fmt.Errorf("no worktree has local main checked out for %s; check out main in a worktree, then retry", root)
	}
	result := Result{MainWorktree: mainWorktree}
	if output, err := service.runner.Run(ctx, mainWorktree, "fetch", "origin", "main"); err != nil {
		return result, fmt.Errorf("fetch origin/main for %s: %s; verify the origin remote and network access, then retry", root, commandError(output, err))
	}
	status, err := service.runner.Run(ctx, mainWorktree, "status", "--porcelain")
	if err != nil {
		return result, fmt.Errorf("check main worktree %s: %s", mainWorktree, commandError(status, err))
	}
	if strings.TrimSpace(string(status)) != "" {
		return result, fmt.Errorf("main worktree is dirty: %s; commit or otherwise resolve those changes yourself, then retry", mainWorktree)
	}
	if output, err := service.runner.Run(ctx, mainWorktree, "merge", "--ff-only", "origin/main"); err != nil {
		return result, fmt.Errorf("main in %s cannot fast-forward to origin/main: %s; reconcile the divergent history yourself, then retry", mainWorktree, commandError(output, err))
	}
	mainSHA, err := service.runner.Run(ctx, mainWorktree, "rev-parse", "main")
	if err != nil {
		return result, fmt.Errorf("resolve local main in %s: %s", mainWorktree, commandError(mainSHA, err))
	}
	originSHA, err := service.runner.Run(ctx, mainWorktree, "rev-parse", "origin/main")
	if err != nil {
		return result, fmt.Errorf("resolve origin/main in %s: %s", mainWorktree, commandError(originSHA, err))
	}
	if strings.TrimSpace(string(mainSHA)) != strings.TrimSpace(string(originSHA)) {
		return result, fmt.Errorf("local main still differs from origin/main in %s; inspect the repository before retrying", mainWorktree)
	}
	return result, nil
}

func mainWorktreePath(output string) string {
	var path string
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/main":
			return path
		case line == "":
			path = ""
		}
	}
	return ""
}

func commandError(output []byte, err error) string {
	if message := strings.TrimSpace(string(output)); message != "" {
		return message
	}
	return err.Error()
}
