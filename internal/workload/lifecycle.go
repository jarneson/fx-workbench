package workload

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func (s *Store) SetProjectClosed(project Entity, workstreams []Entity, closed bool) error {
	status := "active"
	closedAt := ""
	if closed {
		status = "closed"
		closedAt = time.Now().UTC().Format(time.RFC3339)
	}
	entities := append([]Entity{project}, workstreams...)
	type update struct {
		path    string
		content []byte
	}
	updates := make([]update, 0, len(entities))
	for _, entity := range entities {
		if entity.SourcePath == "" {
			return fmt.Errorf("%s %s has no source path", entity.Type, entity.ID)
		}
		content, err := os.ReadFile(entity.SourcePath)
		if err != nil {
			return err
		}
		rewritten, err := updateLifecycleFrontmatter(content, status, closedAt)
		if err != nil {
			return fmt.Errorf("update %s: %w", entity.SourcePath, err)
		}
		updates = append(updates, update{path: entity.SourcePath, content: rewritten})
	}
	for _, item := range updates {
		temporary := item.path + ".lifecycle"
		if err := os.WriteFile(temporary, item.content, 0o644); err != nil {
			return err
		}
		if err := os.Rename(temporary, item.path); err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return nil
}

func (s *Store) SetWorkstreamClosed(workstream Entity, closed bool) error {
	status := "active"
	closedAt := ""
	if closed {
		status = "closed"
		closedAt = time.Now().UTC().Format(time.RFC3339)
	}
	content, err := os.ReadFile(workstream.SourcePath)
	if err != nil {
		return err
	}
	rewritten, err := updateLifecycleFrontmatter(content, status, closedAt)
	if err != nil {
		return err
	}
	temporary := workstream.SourcePath + ".lifecycle"
	if err := os.WriteFile(temporary, rewritten, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, workstream.SourcePath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (s *Store) SetTaskLaunch(task Entity, sessionID string) error {
	return s.setTaskSession(task, sessionID, "coding")
}

// SetTaskSession persists an fx session without changing the task lifecycle.
func (s *Store) SetTaskSession(task Entity, sessionID string) error {
	return s.setTaskSession(task, sessionID, "")
}

func (s *Store) setTaskSession(task Entity, sessionID, status string) error {
	if task.Type != "task" || task.SourcePath == "" || sessionID == "" {
		return errors.New("task source and fx session id are required")
	}
	content, err := os.ReadFile(task.SourcePath)
	if err != nil {
		return err
	}
	fields := map[string]string{
		"fx_session_id": sessionID,
		"updated":       time.Now().UTC().Format(time.RFC3339),
	}
	if status != "" {
		fields["status"] = status
	}
	rewritten, err := updateFrontmatterFields(content, fields)
	if err != nil {
		return err
	}
	temporary := task.SourcePath + ".task-session"
	if err := os.WriteFile(temporary, rewritten, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, task.SourcePath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (s *Store) SetWorkstreamPlannerSession(workstream Entity, sessionID string) error {
	if workstream.Type != "workstream" || workstream.SourcePath == "" || sessionID == "" {
		return errors.New("workstream source and planner session id are required")
	}
	content, err := os.ReadFile(workstream.SourcePath)
	if err != nil {
		return err
	}
	rewritten, err := updateFrontmatterFields(content, map[string]string{
		"planner_session_id": sessionID,
		"updated":            time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	temporary := workstream.SourcePath + ".planner-session"
	if err := os.WriteFile(temporary, rewritten, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, workstream.SourcePath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func updateLifecycleFrontmatter(content []byte, status, closedAt string) ([]byte, error) {
	frontmatter, body, ok := splitFrontmatter(content)
	if !ok {
		return nil, errors.New("cannot update file without frontmatter")
	}
	lines := strings.Split(string(frontmatter), "\n")
	result := make([]string, 0, len(lines)+1)
	seenStatus, seenUpdated := false, false
	updated := time.Now().UTC().Format(time.RFC3339)
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "status:"):
			result = append(result, "status: "+status)
			seenStatus = true
		case strings.HasPrefix(line, "updated:"):
			result = append(result, "updated: "+updated)
			seenUpdated = true
		case strings.HasPrefix(line, "closed_at:"):
			// Reinsert below only for a closed entity.
		default:
			result = append(result, line)
		}
	}
	if !seenStatus {
		result = append(result, "status: "+status)
	}
	if !seenUpdated {
		result = append(result, "updated: "+updated)
	}
	if closedAt != "" {
		result = append(result, "closed_at: "+closedAt)
	}
	return []byte("---\n" + strings.Join(result, "\n") + "\n---\n" + string(body)), nil
}
