package fxassets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jarneson/fx-workbench/internal/principles"
	"gopkg.in/yaml.v3"
)

const manifestName = ".fx-workbench-manifest.json"

type Manifest struct {
	Schema string            `json:"schema"`
	Files  map[string]string `json:"files"`
}

type InstallOptions struct {
	Target        string
	DryRun        bool
	MigrateLegacy bool
	Source        fs.FS
}

type InstallResult struct {
	Target     string   `json:"target"`
	Installed  []string `json:"installed"`
	Unchanged  []string `json:"unchanged"`
	BackedUp   []string `json:"backed_up,omitempty"`
	BackupRoot string   `json:"backup_root,omitempty"`
	DryRun     bool     `json:"dry_run"`
}

type skillMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type profileMetadata struct {
	ID         string   `yaml:"id"`
	Principles []string `yaml:"principles"`
}

func Install(options InstallOptions) (InstallResult, error) {
	if options.Source == nil {
		options.Source = Files()
	}
	if options.Target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return InstallResult{}, err
		}
		options.Target = filepath.Join(home, ".fx")
	}
	target, err := filepath.Abs(options.Target)
	if err != nil {
		return InstallResult{}, err
	}
	assets, err := readAssets(options.Source)
	if err != nil {
		return InstallResult{}, err
	}
	if err := validateAssets(assets); err != nil {
		return InstallResult{}, err
	}
	oldManifest, err := readManifest(filepath.Join(target, manifestName))
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{Target: target, DryRun: options.DryRun}
	newHashes := make(map[string]string, len(assets))
	for _, path := range sortedKeys(assets) {
		content := assets[path]
		hash := contentDigest(content)
		newHashes[path] = hash
		destination := filepath.Join(target, filepath.FromSlash(path))
		current, readErr := os.ReadFile(destination)
		switch {
		case readErr == nil:
			currentHash := contentDigest(current)
			if currentHash == hash {
				result.Unchanged = append(result.Unchanged, path)
				continue
			}
			oldHash, managed := oldManifest.Files[path]
			if !managed {
				if !options.MigrateLegacy {
					return InstallResult{}, fmt.Errorf("refusing to overwrite unmanaged fx file: %s", destination)
				}
				result.Installed = append(result.Installed, path)
				result.BackedUp = append(result.BackedUp, path)
				continue
			}
			if currentHash != oldHash {
				return InstallResult{}, fmt.Errorf("refusing to overwrite user-edited Workbench fx file: %s", destination)
			}
			result.Installed = append(result.Installed, path)
		case errors.Is(readErr, os.ErrNotExist):
			result.Installed = append(result.Installed, path)
		default:
			return InstallResult{}, fmt.Errorf("read install target %s: %w", destination, readErr)
		}
	}
	if options.DryRun {
		return result, nil
	}
	if len(result.BackedUp) > 0 {
		result.BackupRoot = filepath.Join(target, ".fx-workbench-backups", "legacy-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
		for _, path := range result.BackedUp {
			source := filepath.Join(target, filepath.FromSlash(path))
			destination := filepath.Join(result.BackupRoot, filepath.FromSlash(path))
			content, err := os.ReadFile(source)
			if err != nil {
				return InstallResult{}, fmt.Errorf("back up legacy fx file %s: %w", source, err)
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return InstallResult{}, err
			}
			if err := os.WriteFile(destination, content, 0o644); err != nil {
				return InstallResult{}, fmt.Errorf("back up legacy fx file %s: %w", source, err)
			}
		}
	}
	for _, directory := range []string{"skills", "principles", "agent-profiles"} {
		if err := os.MkdirAll(filepath.Join(target, directory), 0o755); err != nil {
			return InstallResult{}, err
		}
	}
	backups := map[string][]byte{}
	created := map[string]bool{}
	rollback := func() {
		for path, content := range backups {
			_ = os.WriteFile(path, content, 0o644)
		}
		for path := range created {
			_ = os.Remove(path)
		}
	}
	for _, path := range result.Installed {
		destination := filepath.Join(target, filepath.FromSlash(path))
		if current, err := os.ReadFile(destination); err == nil {
			backups[destination] = current
		} else if errors.Is(err, os.ErrNotExist) {
			created[destination] = true
		} else {
			rollback()
			return InstallResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			rollback()
			return InstallResult{}, err
		}
		temporary := destination + ".fx-workbench-install"
		if err := os.WriteFile(temporary, assets[path], 0o644); err != nil {
			rollback()
			return InstallResult{}, err
		}
		if err := os.Rename(temporary, destination); err != nil {
			_ = os.Remove(temporary)
			rollback()
			return InstallResult{}, err
		}
	}
	manifest := oldManifest
	manifest.Schema = "fx-workbench/assets/v1"
	if manifest.Files == nil {
		manifest.Files = map[string]string{}
	}
	for path, hash := range newHashes {
		manifest.Files[path] = hash
	}
	manifestContent, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		rollback()
		return InstallResult{}, err
	}
	manifestContent = append(manifestContent, '\n')
	manifestPath := filepath.Join(target, manifestName)
	if err := os.MkdirAll(target, 0o755); err != nil {
		rollback()
		return InstallResult{}, err
	}
	temporaryManifest := manifestPath + ".fx-workbench-install"
	if err := os.WriteFile(temporaryManifest, manifestContent, 0o644); err != nil {
		rollback()
		return InstallResult{}, err
	}
	if err := os.Rename(temporaryManifest, manifestPath); err != nil {
		_ = os.Remove(temporaryManifest)
		rollback()
		return InstallResult{}, err
	}
	return result, nil
}

func readAssets(source fs.FS) (map[string][]byte, error) {
	assets := map[string][]byte{}
	for _, root := range []string{"skills", "principles", "agent-profiles"} {
		err := fs.WalkDir(source, root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() == ".keep" {
				return nil
			}
			content, err := fs.ReadFile(source, path)
			if err != nil {
				return err
			}
			assets[filepath.ToSlash(path)] = content
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read embedded %s: %w", root, err)
		}
	}
	return assets, nil
}

func validateAssets(assets map[string][]byte) error {
	for path, content := range assets {
		if strings.HasPrefix(path, "skills/") && strings.HasSuffix(path, "/SKILL.md") && strings.Count(path, "/") == 2 {
			frontmatter, _, err := splitMarkdown(content)
			if err != nil {
				return fmt.Errorf("validate %s: %w", path, err)
			}
			var metadata skillMetadata
			if err := yaml.Unmarshal(frontmatter, &metadata); err != nil {
				return fmt.Errorf("validate %s: %w", path, err)
			}
			if metadata.Name == "" || metadata.Description == "" {
				return fmt.Errorf("validate %s: skill name and description are required", path)
			}
		}
	}
	for path, content := range assets {
		if strings.HasPrefix(path, "agent-profiles/") && strings.Count(path, "/") == 1 && strings.HasSuffix(path, ".md") {
			frontmatter, _, err := splitMarkdown(content)
			if err != nil {
				return fmt.Errorf("validate %s: %w", path, err)
			}
			var metadata profileMetadata
			if err := yaml.Unmarshal(frontmatter, &metadata); err != nil {
				return fmt.Errorf("validate %s: %w", path, err)
			}
			filenameID := strings.TrimSuffix(filepath.Base(path), ".md")
			if metadata.ID != filenameID || len(metadata.Principles) == 0 {
				return fmt.Errorf("validate %s: profile id must match filename and principles must be non-empty", path)
			}
			seen := map[string]bool{}
			for _, principleID := range metadata.Principles {
				if seen[principleID] {
					return fmt.Errorf("validate %s: duplicate principle %s", path, principleID)
				}
				seen[principleID] = true
				if _, ok := assets["principles/"+principleID+".md"]; !ok {
					return fmt.Errorf("validate %s: unknown bundled principle %s", path, principleID)
				}
			}
		}
	}
	if len(filterPrefix(assets, "principles/")) > 0 {
		temporary, err := os.MkdirTemp("", "fx-workbench-principles-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		for path, content := range filterPrefix(assets, "principles/") {
			destination := filepath.Join(temporary, strings.TrimPrefix(path, "principles/"))
			if err := os.WriteFile(destination, content, 0o644); err != nil {
				return err
			}
		}
		if _, err := principles.New(temporary).List(); err != nil {
			return fmt.Errorf("validate bundled principles: %w", err)
		}
	}
	return nil
}

func readManifest(path string) (Manifest, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{Schema: "fx-workbench/assets/v1", Files: map[string]string{}}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse Workbench fx manifest: %w", err)
	}
	if manifest.Files == nil {
		manifest.Files = map[string]string{}
	}
	return manifest, nil
}

func splitMarkdown(content []byte) ([]byte, string, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", errors.New("missing YAML frontmatter")
	}
	rest := strings.TrimPrefix(text, "---\n")
	index := strings.Index(rest, "\n---\n")
	if index < 0 {
		return nil, "", errors.New("unterminated YAML frontmatter")
	}
	return []byte(rest[:index]), rest[index+5:], nil
}

func filterPrefix(assets map[string][]byte, prefix string) map[string][]byte {
	result := map[string][]byte{}
	for path, content := range assets {
		if strings.HasPrefix(path, prefix) {
			result[path] = content
		}
	}
	return result
}

func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
