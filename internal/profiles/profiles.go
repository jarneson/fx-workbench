package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	fxassets "github.com/jarneson/fx-workbench/fx"
	"github.com/jarneson/fx-workbench/internal/principles"
	"gopkg.in/yaml.v3"
)

const MaxProfileBytes = 16 * 1024

var validID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type metadata struct {
	ID         string   `yaml:"id"`
	Principles []string `yaml:"principles"`
}

type Bundle struct {
	ID           string   `json:"id"`
	PrincipleIDs []string `json:"principles"`
	Text         string   `json:"text"`
	Digest       string   `json:"digest"`
	Source       string   `json:"source"`
}

func Load(id string) (Bundle, error) {
	if !validID.MatchString(id) {
		return Bundle{}, fmt.Errorf("invalid agent profile id %q", id)
	}
	profileContent, source, err := loadProfileContent(id)
	if err != nil {
		return Bundle{}, err
	}
	frontmatter, body, err := splitMarkdown(profileContent)
	if err != nil {
		return Bundle{}, fmt.Errorf("parse agent profile %s: %w", id, err)
	}
	var profile metadata
	if err := yaml.Unmarshal(frontmatter, &profile); err != nil {
		return Bundle{}, fmt.Errorf("parse agent profile metadata %s: %w", id, err)
	}
	if profile.ID != id {
		return Bundle{}, fmt.Errorf("agent profile id %q must match filename %q", profile.ID, id)
	}
	if len(profile.Principles) == 0 {
		return Bundle{}, fmt.Errorf("agent profile %s requires at least one principle", id)
	}
	seen := map[string]bool{}
	items := make([]principles.Principle, 0, len(profile.Principles))
	for _, principleID := range profile.Principles {
		if seen[principleID] {
			return Bundle{}, fmt.Errorf("agent profile %s repeats principle %s", id, principleID)
		}
		seen[principleID] = true
		item, err := loadPrinciple(principleID)
		if err != nil {
			return Bundle{}, fmt.Errorf("agent profile %s: %w", id, err)
		}
		items = append(items, item)
	}
	principleBundle := principles.RenderPrinciples(items)
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- WORKBENCH AGENT PROFILE %s ---\n", id)
	builder.WriteString("This profile applies to the current work and to delegated work. Pass the profile ID and relevant principles to subagents. Apply a principle only when its Use when condition is present; do not narrate principles that did not change a decision.\n")
	if strings.TrimSpace(body) != "" {
		builder.WriteByte('\n')
		builder.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			builder.WriteByte('\n')
		}
	}
	builder.WriteByte('\n')
	builder.WriteString(principleBundle.Text)
	builder.WriteString("--- END WORKBENCH AGENT PROFILE ---\n")
	text := builder.String()
	return Bundle{ID: id, PrincipleIDs: append([]string(nil), profile.Principles...), Text: text, Digest: digest([]byte(text)), Source: source}, nil
}

func loadProfileContent(id string) ([]byte, string, error) {
	root := os.Getenv("FX_WORKBENCH_AGENT_PROFILES_ROOT")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".fx", "agent-profiles")
	}
	path := filepath.Join(root, id+".md")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, "", fmt.Errorf("agent profile must be a regular file: %s", path)
		}
		if info.Size() > MaxProfileBytes {
			return nil, "", fmt.Errorf("agent profile exceeds %d bytes: %s", MaxProfileBytes, path)
		}
		content, err := os.ReadFile(path)
		return content, path, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	embeddedPath := "agent-profiles/" + id + ".md"
	content, err := fs.ReadFile(fxassets.Files(), embeddedPath)
	if err != nil {
		return nil, "", fmt.Errorf("agent profile not found: %s", id)
	}
	if len(content) > MaxProfileBytes {
		return nil, "", fmt.Errorf("agent profile exceeds %d bytes: %s", MaxProfileBytes, embeddedPath)
	}
	return content, "embedded:" + embeddedPath, nil
}

func loadPrinciple(id string) (principles.Principle, error) {
	catalog := principles.New("")
	item, err := catalog.Load(id)
	if err == nil {
		return item, nil
	}
	content, embeddedErr := fs.ReadFile(fxassets.Files(), "principles/"+id+".md")
	if embeddedErr != nil {
		return principles.Principle{}, err
	}
	return principles.Parse(id, "embedded:principles/"+id+".md", content)
}

func splitMarkdown(content []byte) ([]byte, string, error) {
	opening, closing := []byte("---\n"), []byte("\n---\n")
	if bytes.HasPrefix(content, []byte("---\r\n")) {
		opening, closing = []byte("---\r\n"), []byte("\r\n---\r\n")
	} else if !bytes.HasPrefix(content, opening) {
		return nil, "", errors.New("missing YAML frontmatter")
	}
	rest := content[len(opening):]
	index := bytes.Index(rest, closing)
	if index < 0 {
		return nil, "", errors.New("unterminated YAML frontmatter")
	}
	return rest[:index], string(rest[index+len(closing):]), nil
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
