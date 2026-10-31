package workload

import (
	"fmt"
	"regexp"
	"strings"
)

const SchemaV1 = "fx-workbench/v1"

var datePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-`)

type Entity struct {
	Schema           string   `yaml:"schema,omitempty" json:"schema,omitempty"`
	Type             string   `yaml:"type" json:"type"`
	ID               string   `yaml:"id,omitempty" json:"id"`
	Title            string   `yaml:"title,omitempty" json:"title"`
	Status           string   `yaml:"status,omitempty" json:"status"`
	Order            int      `yaml:"order,omitempty" json:"order,omitempty"`
	Project          string   `yaml:"project,omitempty" json:"project,omitempty"`
	Workstream       string   `yaml:"workstream,omitempty" json:"workstream,omitempty"`
	RootRepository   string   `yaml:"root_repository,omitempty" json:"root_repository,omitempty"`
	Repositories     []string `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Repository       string   `yaml:"repository,omitempty" json:"repository,omitempty"`
	Branch           string   `yaml:"branch,omitempty" json:"branch,omitempty"`
	DependsOn        []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Created          string   `yaml:"created,omitempty" json:"created,omitempty"`
	Updated          string   `yaml:"updated,omitempty" json:"updated,omitempty"`
	ClosedAt         string   `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`
	PlannerSessionID string   `yaml:"planner_session_id,omitempty" json:"planner_session_id,omitempty"`
	FXSessionID      string   `yaml:"fx_session_id,omitempty" json:"fx_session_id,omitempty"`
	SourcePath       string   `yaml:"-" json:"source_path"`
	Legacy           bool     `yaml:"-" json:"legacy"`
	Diagnostics      []string `yaml:"-" json:"diagnostics,omitempty"`
}

func (e Entity) Validate() []string {
	var diagnostics []string
	if e.Schema != SchemaV1 {
		diagnostics = append(diagnostics, "legacy or missing schema")
	}
	if e.ID == "" {
		diagnostics = append(diagnostics, "missing stable id")
	}
	if e.Title == "" {
		diagnostics = append(diagnostics, "missing title")
	}
	if e.Type == "project" && e.RootRepository == "" {
		diagnostics = append(diagnostics, "missing root repository")
	}
	if e.Type == "task" && e.Workstream == "" {
		diagnostics = append(diagnostics, "missing workstream")
	}
	if e.Type == "task" && e.Repository == "" {
		diagnostics = append(diagnostics, "missing repository")
	}
	return diagnostics
}

func NormalizeType(value string) string {
	switch value {
	case "agent-task":
		return "task"
	case "technical-plan":
		return "workstream"
	default:
		return value
	}
}

func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	dash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func LegacyID(filename string) string {
	name := strings.TrimSuffix(filename, ".md")
	return datePrefix.ReplaceAllString(name, "")
}

func ValidStatus(status string) bool {
	switch status {
	case "draft", "blocked", "ready", "coding", "self-review", "ready-for-review", "in-review", "active", "done", "merged", "closed", "superseded":
		return true
	default:
		return false
	}
}

func ValidTaskStatus(status string) bool {
	switch status {
	case "draft", "blocked", "ready", "coding", "self-review", "ready-for-review", "in-review", "active", "done", "merged", "superseded":
		return true
	default:
		return false
	}
}

func DependencySatisfied(status string) bool {
	return status == "merged"
}

func ValidateID(id string) error {
	if id == "" || Slug(id) != id {
		return fmt.Errorf("id must be non-empty lowercase kebab-case: %q", id)
	}
	return nil
}
