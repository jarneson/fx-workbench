package workbench

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type RootResult struct {
	ID           string
	Name         string
	WindowID     string
	PaneID       string
	MainWorktree string
	Identity     Identity
	Created      bool
	Adopted      bool
}

type OpenResult struct {
	Session Session
	Created bool
}

func (s *TmuxSource) OpenWorkspace(ctx context.Context, workspace Workspace) (OpenResult, error) {
	if workspace.Kind == WorkspaceRepository {
		root, err := s.EnsureRoot(ctx, workspace.Repository)
		if err != nil {
			return OpenResult{}, err
		}
		session := Session{
			ID: root.ID, Name: root.Name, Role: RoleRoot, WorkspaceID: root.Identity.WorkspaceID,
			Repository: root.Identity.Repository, Worktree: root.Identity.Repository, Path: root.Identity.Repository, Explicit: true,
			Windows: []Window{{ID: root.WindowID, Name: filepath.Base(root.Identity.Repository), Panes: []Pane{{ID: root.PaneID, Path: root.Identity.Repository}}}},
		}
		if err := s.Focus(ctx, session); err != nil {
			return OpenResult{}, err
		}
		return OpenResult{Session: session, Created: root.Created}, nil
	}

	identity, err := NewIdentity(RoleWorkspace, workspace.Repository, workspace.Path, "")
	if err != nil {
		return OpenResult{}, err
	}
	sessions, err := s.workspaceSessions(ctx)
	if err != nil && !errors.Is(err, errSessionNotFound) {
		return OpenResult{}, err
	}
	name := SessionName(identity)
	for _, session := range sessions {
		path := session.Worktree
		if path == "" {
			path = session.Path
		}
		canonical, canonicalErr := canonicalPath(path)
		if canonicalErr == nil && canonical == identity.Worktree {
			if err := s.Focus(ctx, session); err != nil {
				return OpenResult{}, err
			}
			return OpenResult{Session: session}, nil
		}
		if session.Name == name {
			return OpenResult{}, fmt.Errorf("tmux session name %q belongs to a different workspace", name)
		}
	}

	output, err := exec.CommandContext(ctx, s.binary,
		"new-session", "-d", "-s", name, "-c", identity.Worktree, "-n", "shell").CombinedOutput()
	if err != nil {
		// A concurrent opener may have won the race. Reuse only when its canonical
		// path proves that it is the same workspace.
		sessions, listErr := s.workspaceSessions(ctx)
		if listErr == nil {
			for _, session := range sessions {
				path := session.Worktree
				if path == "" {
					path = session.Path
				}
				canonical, canonicalErr := canonicalPath(path)
				if canonicalErr == nil && canonical == identity.Worktree {
					if focusErr := s.Focus(ctx, session); focusErr != nil {
						return OpenResult{}, focusErr
					}
					return OpenResult{Session: session}, nil
				}
			}
		}
		return OpenResult{}, fmt.Errorf("tmux new-session: %s", commandError(output, err))
	}
	id, err := s.sessionID(ctx, name)
	if err != nil {
		return OpenResult{}, fmt.Errorf("resolve created workspace session: %w", err)
	}
	if err := s.TagSession(ctx, id, identity); err != nil {
		_ = exec.CommandContext(context.Background(), s.binary, "kill-session", "-t", id).Run()
		return OpenResult{}, err
	}
	session := Session{ID: id, Name: name, Role: RoleWorkspace, WorkspaceID: identity.WorkspaceID, Repository: identity.Repository, Worktree: identity.Worktree, Path: identity.Worktree, Explicit: true}
	if err := s.Focus(ctx, session); err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Session: session, Created: true}, nil
}

func (s *TmuxSource) workspaceSessions(ctx context.Context) ([]Session, error) {
	format := "#{session_id}\\037#{session_name}\\037#{@fx_workflow_role}\\037#{@fx_repository}\\037#{@fx_worktree}\\037#{pane_current_path}"
	output, err := exec.CommandContext(ctx, s.binary, "list-sessions", "-F", format).CombinedOutput()
	if err != nil {
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
			return nil, errSessionNotFound
		}
		return nil, fmt.Errorf("tmux list-sessions: %s", commandError(output, err))
	}
	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) != 6 {
			continue
		}
		sessions = append(sessions, Session{ID: fields[0], Name: fields[1], Role: fields[2], Repository: fields[3], Worktree: fields[4], Path: fields[5], Explicit: fields[2] != ""})
	}
	return sessions, nil
}

func (s *TmuxSource) EnsureRoot(ctx context.Context, repository string) (RootResult, error) {
	identity, err := NewIdentity(RoleRoot, repository, repository, "")
	if err != nil {
		return RootResult{}, err
	}
	syncResult, err := s.mainSync.Sync(ctx, identity.Repository)
	if err != nil {
		return RootResult{}, fmt.Errorf("synchronize main before opening root session: %w", err)
	}
	sessions, err := s.workspaceSessions(ctx)
	if err != nil && !errors.Is(err, errSessionNotFound) {
		return RootResult{}, err
	}
	paths, err := s.sessionPaths(ctx)
	if err != nil && !errors.Is(err, errSessionNotFound) {
		return RootResult{}, err
	}

	var exact, legacy []Session
	for _, session := range sessions {
		matchesRepository := false
		if canonical, canonicalErr := canonicalPath(session.Repository); canonicalErr == nil && canonical == identity.Repository {
			matchesRepository = true
		}
		for _, path := range paths[session.ID] {
			if canonical, canonicalErr := canonicalPath(path); canonicalErr == nil && canonical == identity.Repository {
				matchesRepository = true
				break
			}
		}
		if !matchesRepository {
			continue
		}
		switch session.Role {
		case RoleRoot:
			exact = append(exact, session)
		case "", RoleWorkspace:
			legacy = append(legacy, session)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = legacy
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for index, session := range matches {
			names[index] = session.Name
		}
		sort.Strings(names)
		return RootResult{}, fmt.Errorf("multiple tmux sessions contain repository %s: %s", identity.Repository, strings.Join(names, ", "))
	}

	created, adopted := false, false
	var id, name string
	if len(matches) == 1 {
		id, name, adopted = matches[0].ID, matches[0].Name, matches[0].Role != RoleRoot
	} else {
		name = safeComponent(filepath.Base(identity.Repository))
		for _, session := range sessions {
			if session.Name != name {
				continue
			}
			name = SessionName(identity)
			break
		}
		if _, lookupErr := s.sessionID(ctx, name); lookupErr == nil {
			return RootResult{}, fmt.Errorf("tmux session name %q belongs to a different repository", name)
		} else if !errors.Is(lookupErr, errSessionNotFound) {
			return RootResult{}, lookupErr
		}
		output, commandErr := exec.CommandContext(ctx, s.binary,
			"new-session", "-d", "-s", name, "-c", identity.Repository, "-n", safeComponent(filepath.Base(identity.Repository))).CombinedOutput()
		if commandErr != nil {
			return RootResult{}, fmt.Errorf("tmux new-session: %s", commandError(output, commandErr))
		}
		id, err = s.sessionID(ctx, name)
		if err != nil {
			return RootResult{}, fmt.Errorf("resolve created root session: %w", err)
		}
		created = true
	}
	windowID, paneID, err := s.ensureRootTerminal(ctx, id, identity.Repository)
	if err != nil {
		return RootResult{}, err
	}
	if err := s.TagSession(ctx, id, identity); err != nil {
		return RootResult{}, err
	}
	return RootResult{ID: id, Name: name, WindowID: windowID, PaneID: paneID, MainWorktree: syncResult.MainWorktree, Identity: identity, Created: created, Adopted: adopted}, nil
}

func (s *TmuxSource) sessionPaths(ctx context.Context) (map[string][]string, error) {
	output, err := exec.CommandContext(ctx, s.binary, "list-panes", "-a", "-F", "#{session_id}\\037#{pane_current_path}").CombinedOutput()
	if err != nil {
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
			return nil, errSessionNotFound
		}
		return nil, fmt.Errorf("tmux list-panes: %s", commandError(output, err))
	}
	paths := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) == 2 {
			paths[fields[0]] = append(paths[fields[0]], fields[1])
		}
	}
	return paths, nil
}

func (s *TmuxSource) ensureRootTerminal(ctx context.Context, sessionID, repository string) (string, string, error) {
	format := "#{window_id}\\037#{window_index}\\037#{window_name}\\037#{pane_id}\\037#{pane_index}\\037#{pane_current_path}"
	output, err := exec.CommandContext(ctx, s.binary, "list-panes", "-s", "-t", sessionID, "-F", format).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("tmux list-panes for repository terminal: %s", commandError(output, err))
	}
	type candidate struct {
		windowID, windowName, paneID, path string
		windowIndex, paneIndex             int
	}
	var panes []candidate
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) != 6 {
			continue
		}
		windowIndex, windowErr := strconv.Atoi(fields[1])
		paneIndex, paneErr := strconv.Atoi(fields[4])
		if windowErr == nil && paneErr == nil {
			panes = append(panes, candidate{fields[0], fields[2], fields[3], fields[5], windowIndex, paneIndex})
		}
	}
	sort.SliceStable(panes, func(i, j int) bool {
		if panes[i].windowIndex != panes[j].windowIndex {
			return panes[i].windowIndex < panes[j].windowIndex
		}
		return panes[i].paneIndex < panes[j].paneIndex
	})
	if len(panes) == 0 {
		return "", "", fmt.Errorf("repository session %s has no panes", sessionID)
	}
	first := panes[0]
	canonical, canonicalErr := canonicalPath(first.path)
	if canonicalErr != nil || canonical != repository {
		return "", "", fmt.Errorf("repository session %s first window is not rooted at %s", sessionID, repository)
	}
	name := safeComponent(filepath.Base(repository))
	if first.windowName != name {
		if output, err := exec.CommandContext(ctx, s.binary, "rename-window", "-t", first.windowID, name).CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("tmux rename repository window: %s", commandError(output, err))
		}
	}
	return first.windowID, first.paneID, nil
}

func (s *TmuxSource) TagSession(ctx context.Context, target string, identity Identity) error {
	if target == "" {
		return errors.New("tmux session target is required")
	}
	if identity.Role == RoleTask && identity.ParentSession == "" {
		identity.ParentSession, _ = s.rootSessionID(ctx, identity.WorkspaceID)
	}
	options := [][2]string{
		{"@fx_workflow_role", identity.Role},
		{"@fx_workspace_id", identity.WorkspaceID},
		{"@fx_repository", identity.Repository},
		{"@fx_worktree", identity.Worktree},
		{"@fx_task_id", identity.TaskID},
		{"@fx_parent_session", identity.ParentSession},
	}
	for _, option := range options {
		args := []string{"set-option", "-t", target}
		if option[1] == "" {
			args = append(args, "-u", option[0])
		} else {
			args = append(args, option[0], option[1])
		}
		if output, err := exec.CommandContext(ctx, s.binary, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux set-option %s: %s", option[0], commandError(output, err))
		}
	}
	return nil
}

var errSessionNotFound = errors.New("tmux session not found")

func (s *TmuxSource) sessionID(ctx context.Context, target string) (string, error) {
	format := "#{session_id}\\037#{session_name}"
	output, err := exec.CommandContext(ctx, s.binary, "list-sessions", "-F", format).CombinedOutput()
	if err != nil {
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
			return "", errSessionNotFound
		}
		return "", fmt.Errorf("tmux list-sessions: %s", commandError(output, err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) == 2 && (fields[0] == target || fields[1] == target) {
			return fields[0], nil
		}
	}
	return "", errSessionNotFound
}

func (s *TmuxSource) rootSessionID(ctx context.Context, workspaceID string) (string, error) {
	format := "#{session_id}\037#{@fx_workflow_role}\037#{@fx_workspace_id}"
	output, err := exec.CommandContext(ctx, s.binary, "list-sessions", "-F", format).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux list-sessions: %s", commandError(output, err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) == 3 && fields[1] == RoleRoot && fields[2] == workspaceID {
			return fields[0], nil
		}
	}
	return "", errSessionNotFound
}

func commandError(output []byte, err error) string {
	if message := strings.TrimSpace(string(output)); message != "" {
		return message
	}
	return err.Error()
}
