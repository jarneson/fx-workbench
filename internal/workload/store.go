package workload

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Store struct {
	Vault string
}

var numberedPrefix = regexp.MustCompile(`^(\d{2})-`)

type ListOptions struct {
	Type          string
	Project       string
	Workstream    string
	Status        string
	IncludeClosed bool
	AllProjects   bool
}

func NewStore() *Store {
	vault := os.Getenv("OBSIDIAN_VAULT")
	if vault == "" {
		home, _ := os.UserHomeDir()
		vault = filepath.Join(home, "Obsidian")
	}
	return &Store{Vault: vault}
}

func fxObsCommand() (string, error) {
	if command := os.Getenv("FX_OBS_BIN"); command != "" {
		return command, nil
	}
	command, err := exec.LookPath("fx-obs")
	if err != nil {
		return "", errors.New("fx-obs is not on PATH; install FX Workbench or set FX_OBS_BIN")
	}
	return command, nil
}

func CurrentProject() (string, error) {
	if project := os.Getenv("FX_WORKBENCH_PROJECT"); project != "" {
		return project, nil
	}
	command, err := fxObsCommand()
	if err != nil {
		return "", err
	}
	output, err := exec.Command(command, "current").Output()
	if err != nil {
		return "", fmt.Errorf("resolve current project: %w", err)
	}
	project := strings.TrimSpace(string(output))
	if project == "" || project == "(unset)" {
		return "", errors.New("no project selected; run fx-obs set <project>")
	}
	return project, nil
}

func (s *Store) List(options ListOptions) ([]Entity, error) {
	project := options.Project
	if project == "" && options.Type != "project" && !options.AllProjects {
		var err error
		project, err = CurrentProject()
		if err != nil {
			return nil, err
		}
	}

	var entities []Entity
	projects, err := s.projectNames(project)
	if err != nil {
		return nil, err
	}
	for _, name := range projects {
		loaded, err := s.loadProject(name)
		if err != nil {
			return nil, err
		}
		if !options.IncludeClosed && options.Project == "" && len(loaded) > 0 && loaded[0].Status == "closed" {
			continue
		}
		entities = append(entities, loaded...)
	}

	closedWorkstreams := make(map[string]bool)
	for _, entity := range entities {
		if entity.Type == "workstream" && entity.Status == "closed" {
			closedWorkstreams[entity.Project+"\x00"+entity.ID] = true
		}
	}
	filtered := entities[:0]
	for _, entity := range entities {
		if !options.IncludeClosed && options.Workstream == "" {
			if entity.Type == "workstream" && entity.Status == "closed" {
				continue
			}
			if entity.Type == "task" && closedWorkstreams[entity.Project+"\x00"+entity.Workstream] {
				continue
			}
		}
		if options.Type != "" && entity.Type != options.Type {
			continue
		}
		if options.Project != "" && entity.Project != options.Project && entity.ID != options.Project {
			continue
		}
		if options.Workstream != "" && entity.Workstream != options.Workstream && entity.ID != options.Workstream {
			continue
		}
		if options.Status != "" && entity.Status != options.Status {
			continue
		}
		filtered = append(filtered, entity)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Type != filtered[j].Type {
			return filtered[i].Type < filtered[j].Type
		}
		if filtered[i].Project != filtered[j].Project {
			return filtered[i].Project < filtered[j].Project
		}
		if filtered[i].Workstream != filtered[j].Workstream {
			return filtered[i].Workstream < filtered[j].Workstream
		}
		if filtered[i].Order != filtered[j].Order && filtered[i].Order > 0 && filtered[j].Order > 0 {
			return filtered[i].Order < filtered[j].Order
		}
		return filtered[i].ID < filtered[j].ID
	})
	return filtered, nil
}

func (s *Store) Find(ref, project string) (Entity, error) {
	if stat, err := os.Stat(ref); err == nil && !stat.IsDir() {
		return parseFile(ref, project)
	}
	if project == "" {
		var err error
		project, err = CurrentProject()
		if err != nil {
			return Entity{}, err
		}
	}
	entities, err := s.List(ListOptions{Project: project, IncludeClosed: true})
	if err != nil {
		return Entity{}, err
	}
	var matches []Entity
	for _, entity := range entities {
		if entity.ID == ref || strings.TrimSuffix(filepath.Base(entity.SourcePath), ".md") == ref {
			matches = append(matches, entity)
		}
	}
	if len(matches) == 0 {
		return Entity{}, fmt.Errorf("workload not found: %s", ref)
	}
	if len(matches) > 1 {
		if ref == project {
			for _, entity := range matches {
				if entity.Type == "project" {
					return entity, nil
				}
			}
		}
		return Entity{}, fmt.Errorf("workload reference is ambiguous: %s", ref)
	}
	return matches[0], nil
}

func (s *Store) FindType(ref, project, entityType string) (Entity, error) {
	entities, err := s.List(ListOptions{Project: project, Type: entityType, IncludeClosed: true})
	if err != nil {
		return Entity{}, err
	}
	for _, entity := range entities {
		if entity.ID == ref || strings.TrimSuffix(filepath.Base(entity.SourcePath), ".md") == ref {
			return entity, nil
		}
	}
	return Entity{}, fmt.Errorf("%s not found: %s", entityType, ref)
}

func (s *Store) Scaffold(entity Entity, objective string) (Entity, error) {
	if err := ValidateID(entity.ID); err != nil {
		return Entity{}, err
	}
	if entity.Title == "" {
		return Entity{}, errors.New("title is required")
	}
	if entity.Status == "" {
		entity.Status = "draft"
	}
	if !ValidStatus(entity.Status) {
		return Entity{}, fmt.Errorf("invalid status: %s", entity.Status)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	entity.Schema = SchemaV1
	entity.Created = now
	entity.Updated = now

	var path, body string
	switch entity.Type {
	case "project":
		if entity.RootRepository == "" {
			return Entity{}, errors.New("root repository is required")
		}
		entity.Project = entity.ID
		if !contains(entity.Repositories, entity.RootRepository) {
			entity.Repositories = append([]string{entity.RootRepository}, entity.Repositories...)
		}
		path = filepath.Join(s.projectPath(entity.ID), "project.md")
		body = "# " + entity.Title + "\n\n## Goal\n\n" + defaultText(objective, "Describe the project outcome.") + "\n\n## Workstreams\n\n"
	case "workstream":
		if entity.Project == "" {
			return Entity{}, errors.New("project is required")
		}
		order, err := nextOrder(s.projectPath(entity.Project), true)
		if err != nil {
			return Entity{}, err
		}
		entity.Order = order
		directory := filepath.Join(s.projectPath(entity.Project), numberedName(order, entity.ID))
		path = filepath.Join(directory, "workstream.md")
		body = "# " + entity.Title + "\n\n## Goal\n\n" + defaultText(objective, "Describe the workstream outcome.") + "\n\n## Scope\n\n## Tasks\n\n"
	case "task":
		if entity.Project == "" || entity.Workstream == "" || entity.Repository == "" {
			return Entity{}, errors.New("project, workstream, and repository are required")
		}
		workstream, err := s.FindType(entity.Workstream, entity.Project, "workstream")
		if err != nil {
			return Entity{}, fmt.Errorf("resolve workstream: %w", err)
		}
		if workstream.Type != "workstream" {
			return Entity{}, fmt.Errorf("task parent is not a workstream: %s", entity.Workstream)
		}
		directory := filepath.Dir(workstream.SourcePath)
		if isLegacyPath(directory) {
			return Entity{}, fmt.Errorf("workstream uses legacy agent-work layout; migrate project %s first", entity.Project)
		}
		order, err := nextOrder(directory, false)
		if err != nil {
			return Entity{}, err
		}
		entity.Order = order
		path = filepath.Join(directory, numberedName(order, shortTaskSlug(entity.ID, entity.Workstream))+".md")
		body = "# " + entity.Title + "\n\n## Objective\n\n" + defaultText(objective, "Describe one concrete outcome.") + "\n\n## Context and evidence\n\n## Scope\n\n## Constraints\n\n## Acceptance criteria\n\n## Validation\n\n## Handoff prompt\n\nExecute this task from the packet, verify current source before editing, and report blockers clearly.\n"
	default:
		return Entity{}, fmt.Errorf("unsupported type: %s", entity.Type)
	}
	if _, err := os.Stat(path); err == nil {
		return Entity{}, fmt.Errorf("workload already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entity{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Entity{}, err
	}
	frontmatter, err := yaml.Marshal(entity)
	if err != nil {
		return Entity{}, err
	}
	content := append([]byte("---\n"), frontmatter...)
	content = append(content, []byte("---\n\n"+body)...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return Entity{}, err
	}
	entity.SourcePath = path
	return entity, nil
}

func (s *Store) SetStatus(entity Entity, status string) error {
	if !ValidStatus(status) {
		return fmt.Errorf("invalid status: %s", status)
	}
	content, err := os.ReadFile(entity.SourcePath)
	if err != nil {
		return err
	}
	updated, err := updateFrontmatter(content, status, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	temporary := entity.SourcePath + ".tmp"
	if err := os.WriteFile(temporary, updated, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, entity.SourcePath)
}

func (s *Store) projectNames(only string) ([]string, error) {
	if only != "" {
		if _, err := os.Stat(filepath.Join(s.Vault, "Projects", only)); err != nil {
			return nil, fmt.Errorf("project does not exist: %s", only)
		}
		return []string{only}, nil
	}
	entries, err := os.ReadDir(filepath.Join(s.Vault, "Projects"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) loadProject(name string) ([]Entity, error) {
	projectPath := s.projectPath(name)
	manifest := filepath.Join(projectPath, "project.md")
	legacyManifest := filepath.Join(s.legacyAgentPath(name), "project.md")
	project := Entity{Type: "project", ID: name, Title: name, Project: name, Status: "active", SourcePath: manifest, Legacy: true}
	manifestToRead := manifest
	if _, err := os.Stat(manifest); errors.Is(err, os.ErrNotExist) {
		manifestToRead = legacyManifest
	}
	if _, err := os.Stat(manifestToRead); err == nil {
		parsed, err := parseFile(manifestToRead, name)
		if err != nil {
			return nil, err
		}
		project = parsed
	}
	entities := []Entity{project}

	if entries, err := os.ReadDir(projectPath); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !numberedPrefix.MatchString(entry.Name()) {
				continue
			}
			directory := filepath.Join(projectPath, entry.Name())
			_ = filepath.WalkDir(directory, func(path string, child fs.DirEntry, walkErr error) error {
				if walkErr != nil || child.IsDir() || filepath.Ext(path) != ".md" {
					return nil
				}
				entity, err := parseFile(path, name)
				if err == nil && (entity.Type == "workstream" || entity.Type == "task") {
					entities = append(entities, entity)
				}
				return nil
			})
		}
	}

	legacyPath := s.legacyAgentPath(name)
	if entries, err := os.ReadDir(legacyPath); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "project.md" {
				continue
			}
			entity, err := parseFile(filepath.Join(legacyPath, entry.Name()), name)
			if err == nil && entity.Type == "workstream" {
				entities = append(entities, entity)
			}
		}
	}
	for _, directory := range []string{filepath.Join(legacyPath, "workstreams"), filepath.Join(legacyPath, "tasks")} {
		_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			entity, err := parseFile(path, name)
			if err == nil {
				entities = append(entities, entity)
			}
			return nil
		})
	}
	return entities, nil
}

func parseFile(path, fallbackProject string) (Entity, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Entity{}, err
	}
	frontmatter, body, ok := splitFrontmatter(content)
	if !ok {
		return Entity{}, fmt.Errorf("missing frontmatter: %s", path)
	}
	var entity Entity
	if err := yaml.Unmarshal(frontmatter, &entity); err != nil {
		return Entity{}, fmt.Errorf("parse %s: %w", path, err)
	}
	entity.Type = NormalizeType(entity.Type)
	var compatibilityDiagnostics []string
	if entity.ID == "" {
		entity.ID = LegacyID(filepath.Base(path))
		entity.Legacy = true
		compatibilityDiagnostics = append(compatibilityDiagnostics, "stable id derived from filename")
	}
	if entity.Title == "" {
		entity.Title = firstHeading(body)
		compatibilityDiagnostics = append(compatibilityDiagnostics, "title derived from first heading")
	}
	if entity.Project == "" {
		entity.Project = fallbackProject
	}
	entity.SourcePath = path
	entity.Diagnostics = append(compatibilityDiagnostics, entity.Validate()...)
	return entity, nil
}

func splitFrontmatter(content []byte) ([]byte, []byte, bool) {
	if !bytes.HasPrefix(content, []byte("---\n")) {
		return nil, content, false
	}
	end := bytes.Index(content[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, content, false
	}
	end += 4
	return content[4:end], content[end+5:], true
}

func updateFrontmatter(content []byte, status, updated string) ([]byte, error) {
	frontmatter, body, ok := splitFrontmatter(content)
	if !ok {
		return nil, errors.New("cannot update file without frontmatter")
	}
	lines := strings.Split(string(frontmatter), "\n")
	seenStatus, seenUpdated := false, false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "status:"):
			lines[i] = "status: " + status
			seenStatus = true
		case strings.HasPrefix(line, "updated:"):
			lines[i] = "updated: " + updated
			seenUpdated = true
		}
	}
	if !seenStatus {
		lines = append(lines, "status: "+status)
	}
	if !seenUpdated {
		lines = append(lines, "updated: "+updated)
	}
	return []byte("---\n" + strings.Join(lines, "\n") + "\n---\n" + string(body)), nil
}

func firstHeading(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func (s *Store) projectPath(project string) string {
	return filepath.Join(s.Vault, "Projects", project)
}

func (s *Store) legacyAgentPath(project string) string {
	return filepath.Join(s.projectPath(project), "agent-work")
}

func nextOrder(directory string, directories bool) (int, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	maximum := 0
	for _, entry := range entries {
		if directories != entry.IsDir() {
			continue
		}
		match := numberedPrefix.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		if !directories {
			entity, err := parseFile(filepath.Join(directory, entry.Name()), "")
			if err != nil || entity.Type != "task" {
				continue
			}
		}
		value, _ := strconv.Atoi(match[1])
		if value > maximum {
			maximum = value
		}
	}
	if maximum >= 99 {
		return 0, fmt.Errorf("two-digit sequence exhausted in %s", directory)
	}
	return maximum + 1, nil
}

func numberedName(order int, slug string) string {
	return fmt.Sprintf("%02d-%s", order, slug)
}

func shortTaskSlug(id, workstream string) string {
	if shortened := strings.TrimPrefix(id, workstream+"-"); shortened != id && shortened != "" {
		return shortened
	}
	return id
}

func isLegacyPath(path string) bool {
	separator := string(os.PathSeparator)
	return strings.Contains(filepath.Clean(path), separator+"agent-work"+separator) || strings.HasSuffix(filepath.Clean(path), separator+"agent-work")
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
