package principles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxFileBytes = 8 * 1024

var validID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Principle struct {
	ID          string `yaml:"id" json:"id"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	Body        string `yaml:"-" json:"body"`
	Path        string `yaml:"-" json:"path"`
	Digest      string `yaml:"-" json:"digest"`
}

type Bundle struct {
	IDs    []string `json:"ids"`
	Text   string   `json:"text"`
	Digest string   `json:"digest"`
}

type Catalog struct {
	Root string
}

func DefaultRoot() string {
	if root := os.Getenv("FX_WORKBENCH_PRINCIPLES_ROOT"); root != "" {
		return root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".fx", "principles")
}

func New(root string) *Catalog {
	if root == "" {
		root = DefaultRoot()
	}
	return &Catalog{Root: root}
}

func ValidID(id string) bool { return validID.MatchString(id) }

func (catalog *Catalog) List() ([]Principle, error) {
	entries, err := os.ReadDir(catalog.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read principle directory: %w", err)
	}
	var result []Principle
	seen := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(catalog.Root, entry.Name())
		principle, err := catalog.loadPath(path, strings.TrimSuffix(entry.Name(), ".md"))
		if err != nil {
			return nil, err
		}
		if previous, ok := seen[principle.ID]; ok {
			return nil, fmt.Errorf("duplicate principle id %q in %s and %s", principle.ID, previous, path)
		}
		seen[principle.ID] = path
		result = append(result, principle)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (catalog *Catalog) Load(id string) (Principle, error) {
	if !ValidID(id) {
		return Principle{}, fmt.Errorf("invalid principle id %q", id)
	}
	root, err := filepath.Abs(catalog.Root)
	if err != nil {
		return Principle{}, err
	}
	path := filepath.Join(root, id+".md")
	if filepath.Dir(path) != root {
		return Principle{}, fmt.Errorf("principle path escapes root: %s", id)
	}
	return catalog.loadPath(path, id)
}

func (catalog *Catalog) loadPath(path, filenameID string) (Principle, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Principle{}, fmt.Errorf("principle not found: %s", filenameID)
		}
		return Principle{}, fmt.Errorf("inspect principle %s: %w", filenameID, err)
	}
	if !info.Mode().IsRegular() {
		return Principle{}, fmt.Errorf("principle must be a regular file: %s", path)
	}
	if info.Size() > MaxFileBytes {
		return Principle{}, fmt.Errorf("principle exceeds %d bytes: %s", MaxFileBytes, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Principle{}, fmt.Errorf("read principle %s: %w", filenameID, err)
	}
	return Parse(filenameID, path, content)
}

func Parse(filenameID, path string, content []byte) (Principle, error) {
	frontmatter, body, err := splitMarkdown(content)
	if err != nil {
		return Principle{}, fmt.Errorf("parse principle %s: %w", filenameID, err)
	}
	var principle Principle
	if err := yaml.Unmarshal(frontmatter, &principle); err != nil {
		return Principle{}, fmt.Errorf("parse principle metadata %s: %w", filenameID, err)
	}
	if principle.ID == "" || principle.Title == "" || principle.Description == "" {
		return Principle{}, fmt.Errorf("principle %s requires string id, title, and description", filenameID)
	}
	if strings.ContainsAny(principle.Title, "\r\n\t") || strings.ContainsAny(principle.Description, "\r\n\t") {
		return Principle{}, fmt.Errorf("principle %s title and description must be single-line text", filenameID)
	}
	if !ValidID(principle.ID) || principle.ID != filenameID {
		return Principle{}, fmt.Errorf("principle id %q must match filename %q", principle.ID, filenameID)
	}
	if strings.TrimSpace(body) == "" {
		return Principle{}, fmt.Errorf("principle body is empty: %s", filenameID)
	}
	principle.Body = body
	principle.Path = path
	principle.Digest = digest(content)
	return principle, nil
}

func (catalog *Catalog) Render(ids []string) (Bundle, error) {
	if len(ids) == 0 {
		return Bundle{}, errors.New("at least one principle id is required")
	}
	seen := map[string]bool{}
	principles := make([]Principle, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return Bundle{}, fmt.Errorf("duplicate requested principle: %s", id)
		}
		seen[id] = true
		principle, err := catalog.Load(id)
		if err != nil {
			return Bundle{}, err
		}
		principles = append(principles, principle)
	}
	return RenderPrinciples(principles), nil
}

func RenderPrinciples(principles []Principle) Bundle {
	ids := make([]string, 0, len(principles))
	for _, principle := range principles {
		ids = append(ids, principle.ID)
	}
	var builder strings.Builder
	builder.WriteString("The user is applying the following principles to the current work.\n\n")
	builder.WriteString("Pause and re-evaluate the current approach before continuing. Repair work already produced when a principle changes a decision. For each principle, state the concrete decision it changed, then continue the user's actual task instead of giving a lecture about principles. These instructions do not widen permissions or override newer user instructions.\n")
	for _, principle := range principles {
		fmt.Fprintf(&builder, "\n--- PRINCIPLE %s (%s) ---\n%s", principle.Title, principle.ID, principle.Body)
		if !strings.HasSuffix(principle.Body, "\n") {
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("\n--- END PRINCIPLES ---\n")
	text := builder.String()
	return Bundle{IDs: append([]string(nil), ids...), Text: text, Digest: digest([]byte(text))}
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
