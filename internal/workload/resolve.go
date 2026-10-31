package workload

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type ProjectResolution struct {
	Project       Entity   `json:"project"`
	WorkspaceRoot string   `json:"workspace_root,omitempty"`
	Remote        string   `json:"remote,omitempty"`
	Reason        string   `json:"reason"`
	Candidates    []string `json:"candidates,omitempty"`
}

func (s *Store) ResolveProject(cwd, explicit string) (ProjectResolution, error) {
	if explicit != "" {
		project, err := s.FindType(explicit, explicit, "project")
		if err != nil {
			return ProjectResolution{}, err
		}
		return ProjectResolution{Project: project, Reason: "explicit"}, nil
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ProjectResolution{}, err
		}
	}
	selected, _ := CurrentProject()
	root, remote, gitErr := workspaceIdentity(cwd)
	if gitErr == nil {
		projects, err := s.List(ListOptions{Type: "project"})
		if err != nil {
			return ProjectResolution{}, err
		}
		matches := matchProjects(projects, root, remote)
		for _, ranked := range []struct {
			candidates []Entity
			reason     string
		}{
			{matches.rootPath, "root-repository-path"},
			{matches.repositoryPath, "repository-path"},
			{matches.rootRemote, "root-repository-remote"},
			{matches.repositoryRemote, "repository-remote"},
		} {
			if project, ok := chooseProject(ranked.candidates, selected); ok {
				return ProjectResolution{
					Project: project, WorkspaceRoot: root, Remote: remote,
					Reason: ranked.reason, Candidates: projectIDs(ranked.candidates),
				}, nil
			}
			if len(ranked.candidates) > 1 {
				return ProjectResolution{}, fmt.Errorf("workspace matches multiple projects at %s rank: %s; select one with fx-obs set", ranked.reason, strings.Join(projectIDs(ranked.candidates), ", "))
			}
		}
		return ProjectResolution{}, errors.New("no project registers the current Git workspace; onboard it or use --project")
	}
	if selected != "" {
		project, err := s.FindType(selected, selected, "project")
		if err == nil {
			return ProjectResolution{Project: project, WorkspaceRoot: root, Remote: remote, Reason: "selected-project"}, nil
		}
	}
	if gitErr != nil {
		return ProjectResolution{}, fmt.Errorf("resolve workspace Git repository: %w", gitErr)
	}
	return ProjectResolution{}, errors.New("no project registers the current workspace; onboard it or use --project")
}

func workspaceIdentity(cwd string) (string, string, error) {
	output, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	root := strings.TrimSpace(string(output))
	remoteOutput, remoteErr := exec.Command("git", "-C", root, "config", "--get", "remote.origin.url").Output()
	remote := ""
	if remoteErr == nil {
		remote = normalizeRemote(strings.TrimSpace(string(remoteOutput)))
	}
	return root, remote, nil
}

type projectMatches struct {
	rootPath         []Entity
	repositoryPath   []Entity
	rootRemote       []Entity
	repositoryRemote []Entity
}

func matchProjects(projects []Entity, workspaceRoot, remote string) projectMatches {
	var matches projectMatches
	canonicalWorkspace := canonicalPath(workspaceRoot)
	for _, project := range projects {
		if project.RootRepository != "" && canonicalPath(project.RootRepository) == canonicalWorkspace {
			matches.rootPath = append(matches.rootPath, project)
		}
		for _, repository := range project.Repositories {
			if canonicalPath(repository) == canonicalWorkspace {
				matches.repositoryPath = append(matches.repositoryPath, project)
				break
			}
		}
	}
	matches.rootPath = uniqueProjects(matches.rootPath)
	matches.repositoryPath = uniqueProjects(matches.repositoryPath)
	if len(matches.rootPath) > 0 || len(matches.repositoryPath) > 0 || remote == "" {
		return matches
	}

	for _, project := range projects {
		if project.RootRepository != "" {
			_, rootRemote, err := workspaceIdentity(project.RootRepository)
			if err == nil && rootRemote == remote {
				matches.rootRemote = append(matches.rootRemote, project)
			}
		}
		for _, repository := range project.Repositories {
			_, repositoryRemote, err := workspaceIdentity(repository)
			if err == nil && repositoryRemote == remote {
				matches.repositoryRemote = append(matches.repositoryRemote, project)
				break
			}
		}
	}
	matches.rootRemote = uniqueProjects(matches.rootRemote)
	matches.repositoryRemote = uniqueProjects(matches.repositoryRemote)
	return matches
}

func chooseProject(candidates []Entity, selected string) (Entity, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	for _, candidate := range candidates {
		if candidate.ID == selected {
			return candidate, true
		}
	}
	return Entity{}, false
}

func uniqueProjects(projects []Entity) []Entity {
	seen := make(map[string]bool, len(projects))
	result := projects[:0]
	for _, project := range projects {
		if !seen[project.ID] {
			seen[project.ID] = true
			result = append(result, project)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func projectIDs(projects []Entity) []string {
	ids := make([]string, len(projects))
	for index, project := range projects {
		ids[index] = project.ID
	}
	return ids
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}

func normalizeRemote(remote string) string {
	remote = strings.TrimSpace(strings.TrimSuffix(remote, ".git"))
	remote = strings.TrimPrefix(remote, "ssh://")
	remote = strings.TrimPrefix(remote, "https://")
	remote = strings.TrimPrefix(remote, "http://")
	if at := strings.LastIndex(remote, "@"); at >= 0 {
		remote = remote[at+1:]
	}
	if colon := strings.Index(remote, ":"); colon >= 0 && !strings.Contains(remote[:colon], "/") {
		remote = remote[:colon] + "/" + remote[colon+1:]
	}
	return strings.ToLower(strings.TrimSuffix(remote, "/"))
}
