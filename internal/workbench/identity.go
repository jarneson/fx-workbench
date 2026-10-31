package workbench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	RoleRoot       = "root"
	RoleTask       = "task"
	RoleWorkstream = "workstream"
	RoleWorkspace  = "workspace"
)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

type Identity struct {
	Role          string
	WorkspaceID   string
	Repository    string
	Worktree      string
	TaskID        string
	ParentSession string
	Explicit      bool
}

// WorkstreamIdentity is the shared tmux session contract for a planner and all
// task workers belonging to one canonical project/workstream/repository tuple.
type WorkstreamIdentity struct {
	Project     string
	Workstream  string
	Repository  string
	WorkspaceID string
	SessionName string
}

func NewWorkstreamIdentity(project, workstream, repository string) (WorkstreamIdentity, error) {
	if project == "" || workstream == "" {
		return WorkstreamIdentity{}, fmt.Errorf("project and workstream are required")
	}
	repository, err := canonicalPath(repository)
	if err != nil {
		return WorkstreamIdentity{}, fmt.Errorf("canonical repository: %w", err)
	}
	key := project + "\x00" + workstream + "\x00" + repository
	return WorkstreamIdentity{
		Project: project, Workstream: workstream, Repository: repository,
		WorkspaceID: "ws-" + pathHash(repository),
		SessionName: fmt.Sprintf("tasks-%s-%s", safeComponent(workstream), pathHash(key)),
	}, nil
}

func (identity WorkstreamIdentity) SessionOptions() [][2]string {
	return [][2]string{
		{"@fx_workflow_role", RoleWorkstream},
		{"@fx_workspace_id", identity.WorkspaceID},
		{"@fx_project_id", identity.Project},
		{"@fx_workstream_id", identity.Workstream},
		{"@fx_repository", identity.Repository},
	}
}

func NewIdentity(role, repository, worktree, taskID string) (Identity, error) {
	if role != RoleRoot && role != RoleTask && role != RoleWorkstream && role != RoleWorkspace {
		return Identity{}, fmt.Errorf("invalid workflow role %q", role)
	}
	repository, err := canonicalPath(repository)
	if err != nil {
		return Identity{}, fmt.Errorf("canonical repository: %w", err)
	}
	if worktree == "" {
		worktree = repository
	}
	worktree, err = canonicalPath(worktree)
	if err != nil {
		return Identity{}, fmt.Errorf("canonical worktree: %w", err)
	}
	return Identity{
		Role: role, WorkspaceID: "ws-" + pathHash(repository), Repository: repository,
		Worktree: worktree, TaskID: taskID, Explicit: true,
	}, nil
}

func SessionName(identity Identity) string {
	repository := safeComponent(filepath.Base(identity.Repository))
	if identity.Role == RoleRoot {
		return fmt.Sprintf("root-%s-%s", repository, pathHash(identity.Repository))
	}
	worktree := safeComponent(filepath.Base(identity.Worktree))
	return fmt.Sprintf("%s-%s-%s", repository, worktree, pathHash(identity.Worktree))
}

func inferIdentity(session *Session) {
	if session.Role != "" {
		session.Explicit = true
		return
	}
	session.Explicit = false
	path := filepath.Clean(session.Path)
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "dev" && parts[i+1] == "worktrees" {
			session.Role = "worktree"
			session.Repository = parts[i+2]
			session.Worktree = path
			return
		}
	}
	session.Role = RoleWorkspace
	session.Repository = path
	session.Worktree = path
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}

func pathHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:10]
}

func safeComponent(value string) string {
	value = strings.Trim(unsafeName.ReplaceAllString(value, "-"), "-_")
	if value == "" {
		value = "workspace"
	}
	const maxLength = 32
	if len(value) > maxLength {
		value = value[:maxLength]
	}
	return value
}
