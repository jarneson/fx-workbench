package workload

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type OnboardOptions struct {
	ID                  string
	Title               string
	Objective           string
	RootRepository      string
	Repositories        []string
	WorkstreamID        string
	WorkstreamTitle     string
	WorkstreamObjective string
	FromProject         string
	MigrateWorkstream   string
	Select              bool
	DryRun              bool
}

type Migration struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Order       int    `json:"order"`
	SourcePath  string `json:"source_path"`
	Destination string `json:"destination"`
}

type OnboardResult struct {
	Project      Entity      `json:"project"`
	Workstream   Entity      `json:"workstream"`
	Repositories []string    `json:"repositories"`
	Migrations   []Migration `json:"migrations,omitempty"`
	Selected     bool        `json:"selected"`
	DryRun       bool        `json:"dry_run"`
}

func (s *Store) Onboard(options OnboardOptions) (OnboardResult, error) {
	if err := ValidateID(options.ID); err != nil {
		return OnboardResult{}, err
	}
	if options.Title == "" {
		return OnboardResult{}, errors.New("project title is required")
	}
	if options.WorkstreamID == "" {
		if options.MigrateWorkstream != "" {
			options.WorkstreamID = options.MigrateWorkstream
		} else {
			options.WorkstreamID = "core"
		}
	}
	if err := ValidateID(options.WorkstreamID); err != nil {
		return OnboardResult{}, fmt.Errorf("workstream %w", err)
	}
	if options.WorkstreamTitle == "" {
		options.WorkstreamTitle = "Core delivery"
	}

	root, err := validateGitRoot(options.RootRepository)
	if err != nil {
		return OnboardResult{}, fmt.Errorf("root repository: %w", err)
	}
	repositories := []string{root}
	for _, candidate := range options.Repositories {
		repository, err := validateGitRoot(candidate)
		if err != nil {
			return OnboardResult{}, fmt.Errorf("repository %q: %w", candidate, err)
		}
		if !contains(repositories, repository) {
			repositories = append(repositories, repository)
		}
	}

	projectPath := filepath.Join(s.projectPath(options.ID), "project.md")
	if _, err := os.Stat(projectPath); err == nil {
		return OnboardResult{}, fmt.Errorf("onboarding target already exists: %s", projectPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return OnboardResult{}, err
	}

	migrations, err := s.planMigrations(options.FromProject, options.MigrateWorkstream, options.ID)
	if err != nil {
		return OnboardResult{}, err
	}
	workstreamOrder, err := nextOrder(s.projectPath(options.ID), true)
	if err != nil {
		return OnboardResult{}, err
	}
	workstreamDirectory := filepath.Join(s.projectPath(options.ID), numberedName(workstreamOrder, options.WorkstreamID))
	workstreamPath := filepath.Join(workstreamDirectory, "workstream.md")
	for _, migration := range migrations {
		if migration.Type == "workstream" {
			workstreamPath = migration.Destination
			break
		}
	}
	if len(migrations) == 0 {
		if _, err := os.Stat(workstreamPath); err == nil {
			return OnboardResult{}, fmt.Errorf("onboarding target already exists: %s", workstreamPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return OnboardResult{}, err
		}
	}
	result := OnboardResult{
		Project: Entity{
			Schema: SchemaV1, Type: "project", ID: options.ID, Title: options.Title,
			Status: "active", Project: options.ID, RootRepository: root,
			Repositories: repositories, SourcePath: projectPath,
		},
		Workstream: Entity{
			Schema: SchemaV1, Type: "workstream", ID: options.WorkstreamID, Order: workstreamOrder,
			Title: options.WorkstreamTitle, Status: "active", Project: options.ID,
			Repository: root, SourcePath: workstreamPath,
		},
		Repositories: repositories,
		Migrations:   migrations,
		DryRun:       options.DryRun,
	}
	if options.DryRun {
		return result, nil
	}

	project, err := s.Scaffold(result.Project, options.Objective)
	if err != nil {
		return OnboardResult{}, err
	}
	result.Project = project
	if len(migrations) == 0 {
		workstream, err := s.Scaffold(result.Workstream, options.WorkstreamObjective)
		if err != nil {
			return OnboardResult{}, fmt.Errorf("project created at %s but workstream creation failed: %w", project.SourcePath, err)
		}
		result.Workstream = workstream
	}

	for _, migration := range migrations {
		if err := migrateFile(migration, options.ID); err != nil {
			return result, fmt.Errorf("project created but migration %s failed: %w", migration.ID, err)
		}
	}
	if len(migrations) > 0 {
		workstream, err := parseFile(workstreamPath, options.ID)
		if err != nil {
			return result, fmt.Errorf("migrated workstream cannot be loaded: %w", err)
		}
		result.Workstream = workstream
	}
	if options.Select {
		if err := selectProject(options.ID); err != nil {
			return result, fmt.Errorf("project onboarded but selection failed: %w", err)
		}
		result.Selected = true
	}
	return result, nil
}

func (s *Store) planMigrations(fromProject, workstreamID, targetProject string) ([]Migration, error) {
	if fromProject == "" && workstreamID == "" {
		return nil, nil
	}
	if fromProject == "" || workstreamID == "" {
		return nil, errors.New("--from-project and --migrate-workstream must be used together")
	}
	if fromProject == targetProject {
		return nil, errors.New("migration source and target project must differ")
	}
	entities, err := s.List(ListOptions{Project: fromProject})
	if err != nil {
		return nil, err
	}
	var selected []Entity
	for _, entity := range entities {
		if (entity.Type == "workstream" && entity.ID == workstreamID) || (entity.Type == "task" && entity.Workstream == workstreamID) {
			if entity.Schema != SchemaV1 {
				return nil, fmt.Errorf("cannot migrate legacy workload %s; canonicalize it first", entity.ID)
			}
			selected = append(selected, entity)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("workstream not found in project %s: %s", fromProject, workstreamID)
	}
	var workstream Entity
	var tasks []Entity
	for _, entity := range selected {
		switch entity.Type {
		case "workstream":
			workstream = entity
		case "task":
			tasks = append(tasks, entity)
		}
	}
	if workstream.ID == "" {
		return nil, fmt.Errorf("canonical workstream entity not found: %s", workstreamID)
	}
	workstreamOrder, err := nextOrder(s.projectPath(targetProject), true)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(s.projectPath(targetProject), numberedName(workstreamOrder, workstreamID))
	migrations := []Migration{{
		ID: workstream.ID, Type: workstream.Type, Order: workstreamOrder,
		SourcePath:  workstream.SourcePath,
		Destination: filepath.Join(directory, "workstream.md"),
	}}
	tasks = orderTasks(tasks)
	for index, task := range tasks {
		order := index + 1
		migrations = append(migrations, Migration{
			ID: task.ID, Type: task.Type, Order: order, SourcePath: task.SourcePath,
			Destination: filepath.Join(directory, numberedName(order, shortTaskSlug(task.ID, workstreamID))+".md"),
		})
	}
	for _, migration := range migrations {
		if _, err := os.Stat(migration.Destination); err == nil {
			return nil, fmt.Errorf("migration destination exists: %s", migration.Destination)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return migrations, nil
}

func migrateFile(migration Migration, targetProject string) error {
	content, err := os.ReadFile(migration.SourcePath)
	if err != nil {
		return err
	}
	updated, err := updateFrontmatterFields(content, map[string]string{
		"project": targetProject,
		"order":   strconv.Itoa(migration.Order),
		"updated": utcNow(),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(migration.Destination), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(migration.Destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(updated); err != nil {
		_ = file.Close()
		_ = os.Remove(migration.Destination)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(migration.Destination)
		return err
	}
	redirect := fmt.Sprintf("# Moved: %s\n\nCanonical workload moved to:\n\n`%s`\n", migration.ID, migration.Destination)
	temporary := migration.SourcePath + ".migrating"
	if err := os.WriteFile(temporary, []byte(redirect), 0o644); err != nil {
		_ = os.Remove(migration.Destination)
		return err
	}
	if err := os.Rename(temporary, migration.SourcePath); err != nil {
		_ = os.Remove(migration.Destination)
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func updateFrontmatterFields(content []byte, values map[string]string) ([]byte, error) {
	frontmatter, body, ok := splitFrontmatter(content)
	if !ok {
		return nil, errors.New("cannot update file without frontmatter")
	}
	lines := strings.Split(string(frontmatter), "\n")
	seen := make(map[string]bool, len(values))
	for i, line := range lines {
		for key, value := range values {
			if strings.HasPrefix(line, key+":") {
				lines[i] = key + ": " + value
				seen[key] = true
			}
		}
	}
	for key, value := range values {
		if !seen[key] {
			lines = append(lines, key+": "+value)
		}
	}
	return []byte("---\n" + strings.Join(lines, "\n") + "\n---\n" + string(body)), nil
}

func validateGitRoot(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	output, err := exec.Command("git", "-C", absolute, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("not a git checkout: %s", strings.TrimSpace(string(output)))
	}
	root := strings.TrimSpace(string(output))
	canonicalInput, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if canonicalRoot != canonicalInput {
		return "", fmt.Errorf("path is inside %s; pass the repository root", root)
	}
	return root, nil
}

func selectProject(project string) error {
	command, err := fxObsCommand()
	if err != nil {
		return err
	}
	if output, err := exec.Command(command, "set", project).CombinedOutput(); err != nil {
		return fmt.Errorf("fx-obs set: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func utcNow() string {
	return timeNow().UTC().Format("2006-01-02T15:04:05Z")
}

var timeNow = time.Now
