package workload

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type LayoutMove struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Order       int    `json:"order,omitempty"`
	SourcePath  string `json:"source_path"`
	Destination string `json:"destination"`
}

type LayoutResult struct {
	Project string       `json:"project"`
	Moves   []LayoutMove `json:"moves"`
	DryRun  bool         `json:"dry_run"`
}

func (s *Store) MigrateLayout(project string, dryRun bool) (LayoutResult, error) {
	if project == "" {
		var err error
		project, err = CurrentProject()
		if err != nil {
			return LayoutResult{}, err
		}
	}
	entities, err := s.List(ListOptions{Project: project})
	if err != nil {
		return LayoutResult{}, err
	}
	var projectEntity Entity
	var workstreams []Entity
	var tasks []Entity
	for _, entity := range entities {
		if entity.Schema != SchemaV1 {
			continue
		}
		switch entity.Type {
		case "project":
			projectEntity = entity
		case "workstream":
			workstreams = append(workstreams, entity)
		case "task":
			tasks = append(tasks, entity)
		}
	}
	if projectEntity.ID == "" {
		return LayoutResult{}, fmt.Errorf("canonical project manifest not found: %s", project)
	}
	sort.SliceStable(workstreams, func(i, j int) bool {
		if workstreams[i].Order != workstreams[j].Order && workstreams[i].Order > 0 && workstreams[j].Order > 0 {
			return workstreams[i].Order < workstreams[j].Order
		}
		if workstreams[i].Created != workstreams[j].Created {
			return workstreams[i].Created < workstreams[j].Created
		}
		return workstreams[i].ID < workstreams[j].ID
	})

	result := LayoutResult{Project: project, DryRun: dryRun}
	projectDestination := filepath.Join(s.projectPath(project), "project.md")
	if filepath.Clean(projectEntity.SourcePath) != filepath.Clean(projectDestination) {
		result.Moves = append(result.Moves, LayoutMove{
			ID: projectEntity.ID, Type: "project", SourcePath: projectEntity.SourcePath, Destination: projectDestination,
		})
	}

	for workstreamIndex, workstream := range workstreams {
		workstreamOrder := workstreamIndex + 1
		directory := filepath.Join(s.projectPath(project), numberedName(workstreamOrder, workstream.ID))
		workstreamDestination := filepath.Join(directory, "workstream.md")
		if filepath.Clean(workstream.SourcePath) != filepath.Clean(workstreamDestination) || workstream.Order != workstreamOrder {
			result.Moves = append(result.Moves, LayoutMove{
				ID: workstream.ID, Type: "workstream", Order: workstreamOrder,
				SourcePath: workstream.SourcePath, Destination: workstreamDestination,
			})
		}
		var workstreamTasks []Entity
		for _, task := range tasks {
			if task.Workstream == workstream.ID {
				workstreamTasks = append(workstreamTasks, task)
			}
		}
		workstreamTasks = orderTasks(workstreamTasks)
		for taskIndex, task := range workstreamTasks {
			taskOrder := taskIndex + 1
			taskDestination := filepath.Join(directory, numberedName(taskOrder, shortTaskSlug(task.ID, workstream.ID))+".md")
			if filepath.Clean(task.SourcePath) != filepath.Clean(taskDestination) || task.Order != taskOrder {
				result.Moves = append(result.Moves, LayoutMove{
					ID: task.ID, Type: "task", Order: taskOrder,
					SourcePath: task.SourcePath, Destination: taskDestination,
				})
			}
		}
	}

	for _, move := range result.Moves {
		if filepath.Clean(move.SourcePath) == filepath.Clean(move.Destination) {
			continue
		}
		if _, err := os.Stat(move.Destination); err == nil {
			return LayoutResult{}, fmt.Errorf("layout destination exists: %s", move.Destination)
		} else if !errors.Is(err, os.ErrNotExist) {
			return LayoutResult{}, err
		}
	}
	if dryRun {
		return result, nil
	}
	for _, move := range result.Moves {
		if err := applyLayoutMove(move); err != nil {
			return result, fmt.Errorf("move %s failed: %w", move.ID, err)
		}
	}
	removeEmptyLegacyDirectories(s.legacyAgentPath(project))
	return result, nil
}

func orderTasks(tasks []Entity) []Entity {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Order != tasks[j].Order && tasks[i].Order > 0 && tasks[j].Order > 0 {
			return tasks[i].Order < tasks[j].Order
		}
		if tasks[i].Created != tasks[j].Created {
			return tasks[i].Created < tasks[j].Created
		}
		return tasks[i].ID < tasks[j].ID
	})
	remaining := append([]Entity(nil), tasks...)
	result := make([]Entity, 0, len(tasks))
	emitted := make(map[string]bool, len(tasks))
	inSet := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		inSet[task.ID] = true
	}
	for len(remaining) > 0 {
		selected := -1
		for index, task := range remaining {
			ready := true
			for _, dependency := range task.DependsOn {
				if inSet[dependency] && !emitted[dependency] {
					ready = false
					break
				}
			}
			if ready {
				selected = index
				break
			}
		}
		if selected < 0 {
			// Keep deterministic order when legacy data contains a dependency cycle.
			result = append(result, remaining...)
			break
		}
		task := remaining[selected]
		result = append(result, task)
		emitted[task.ID] = true
		remaining = append(remaining[:selected], remaining[selected+1:]...)
	}
	return result
}

func applyLayoutMove(move LayoutMove) error {
	content, err := os.ReadFile(move.SourcePath)
	if err != nil {
		return err
	}
	if move.Order > 0 {
		content, err = updateFrontmatterFields(content, map[string]string{
			"order":   strconv.Itoa(move.Order),
			"updated": utcNow(),
		})
		if err != nil {
			return err
		}
	}
	if filepath.Clean(move.SourcePath) == filepath.Clean(move.Destination) {
		temporary := move.SourcePath + ".layout-migration"
		if err := os.WriteFile(temporary, content, 0o644); err != nil {
			return err
		}
		if err := os.Rename(temporary, move.SourcePath); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(move.Destination), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(move.Destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(move.Destination)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(move.Destination)
		return err
	}
	if err := os.Remove(move.SourcePath); err != nil {
		_ = os.Remove(move.Destination)
		return err
	}
	return nil
}

func removeEmptyLegacyDirectories(root string) {
	for _, path := range []string{
		filepath.Join(root, "tasks", "completed"),
		filepath.Join(root, "tasks"),
		filepath.Join(root, "workstreams"),
		root,
	} {
		_ = os.Remove(path)
	}
}
