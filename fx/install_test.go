package fxassets

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInstallDryRunAndManagedUpdates(t *testing.T) {
	target := t.TempDir()
	source := testAssets("first")
	dry, err := Install(InstallOptions{Target: target, DryRun: true, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Installed) != 2 {
		t.Fatalf("dry installed = %#v", dry.Installed)
	}
	if _, err := os.Stat(filepath.Join(target, "skills", "demo", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote files: %v", err)
	}
	first, err := Install(InstallOptions{Target: target, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Installed) != 2 {
		t.Fatalf("installed = %#v", first.Installed)
	}
	second, err := Install(InstallOptions{Target: target, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Unchanged) != 2 || len(second.Installed) != 0 {
		t.Fatalf("second = %#v", second)
	}
	updated, err := Install(InstallOptions{Target: target, Source: testAssets("second")})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Installed) != 1 {
		t.Fatalf("updated = %#v", updated)
	}
	content, err := os.ReadFile(filepath.Join(target, "skills", "demo", "SKILL.md"))
	if err != nil || !strings.Contains(string(content), "second") {
		t.Fatalf("content = %q, err = %v", content, err)
	}
}

func TestInstallAdoptsIdenticalExistingFile(t *testing.T) {
	target := t.TempDir()
	content, err := fs.ReadFile(testAssets("first"), "skills/demo/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallOptions{Target: target, Source: testAssets("first")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Unchanged) != 1 || result.Unchanged[0] != "skills/demo/SKILL.md" {
		t.Fatalf("result = %#v", result)
	}
	manifest, err := readManifest(filepath.Join(target, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files["skills/demo/SKILL.md"] == "" {
		t.Fatal("identical existing file was not adopted into manifest")
	}
}

func TestInstallProtectsUnmanagedAndEditedFiles(t *testing.T) {
	t.Run("unmanaged", func(t *testing.T) {
		target := t.TempDir()
		path := filepath.Join(target, "skills", "demo", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("personal"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Install(InstallOptions{Target: target, Source: testAssets("first")})
		if err == nil || !strings.Contains(err.Error(), "unmanaged") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("edited managed", func(t *testing.T) {
		target := t.TempDir()
		if _, err := Install(InstallOptions{Target: target, Source: testAssets("first")}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(target, "skills", "demo", "SKILL.md")
		if err := os.WriteFile(path, []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Install(InstallOptions{Target: target, Source: testAssets("second")})
		if err == nil || !strings.Contains(err.Error(), "user-edited") {
			t.Fatalf("err = %v", err)
		}
		content, _ := os.ReadFile(path)
		if string(content) != "edited" {
			t.Fatalf("edited file changed: %q", content)
		}
	})
}

func TestInstallMigratesLegacyFileWithBackup(t *testing.T) {
	target := t.TempDir()
	path := filepath.Join(target, "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("legacy personal copy")
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallOptions{Target: target, Source: testAssets("first"), MigrateLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.BackedUp) != 1 || result.BackedUp[0] != "skills/demo/SKILL.md" || result.BackupRoot == "" {
		t.Fatalf("result = %#v", result)
	}
	backup, err := os.ReadFile(filepath.Join(result.BackupRoot, "skills", "demo", "SKILL.md"))
	if err != nil || string(backup) != string(legacy) {
		t.Fatalf("backup = %q, err = %v", backup, err)
	}
	installed, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(installed), "Demo skill first") {
		t.Fatalf("installed = %q, err = %v", installed, err)
	}
}

func TestInstallPreservesUnrelatedFiles(t *testing.T) {
	target := t.TempDir()
	unrelated := filepath.Join(target, "skills", "personal", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(unrelated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("personal"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{Target: target, Source: testAssets("first")}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(unrelated)
	if err != nil || string(content) != "personal" {
		t.Fatalf("unrelated = %q, err = %v", content, err)
	}
	for _, directory := range []string{"principles", "agent-profiles"} {
		if info, err := os.Stat(filepath.Join(target, directory)); err != nil || !info.IsDir() {
			t.Fatalf("missing %s directory: %v", directory, err)
		}
	}
}

func TestInstallValidatesBeforeWriting(t *testing.T) {
	target := t.TempDir()
	invalid := fstest.MapFS{
		"skills/demo/SKILL.md": &fstest.MapFile{Data: []byte("no frontmatter")},
	}
	if _, err := Install(InstallOptions{Target: target, Source: invalid}); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("err = %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid install wrote %#v", entries)
	}
}

func TestInstallRejectsProfileWithUnknownPrinciple(t *testing.T) {
	target := t.TempDir()
	invalid := fstest.MapFS{
		"agent-profiles/coding.md": &fstest.MapFile{Data: []byte("---\nid: coding\nprinciples:\n  - missing\n---\n")},
	}
	if _, err := Install(InstallOptions{Target: target, Source: invalid}); err == nil || !strings.Contains(err.Error(), "unknown bundled principle") {
		t.Fatalf("err = %v", err)
	}
}

func TestBundledLauncherUsesPublicWorkbenchPath(t *testing.T) {
	target := t.TempDir()
	if _, err := Install(InstallOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(target, "skills", "worktree-task-launcher", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "fx-workbench launch --dry-run") || !strings.Contains(text, "fx-workbench launch [") {
		t.Fatalf("launcher does not document the public launch command:\n%s", text)
	}
	for _, unavailable := range []string{"fx-task-worktree", "git-worktree-sessionizer", "pnpm install"} {
		if strings.Contains(text, unavailable) {
			t.Errorf("launcher references unavailable command %q", unavailable)
		}
	}
}

func testAssets(marker string) fs.FS {
	return fstest.MapFS{
		"skills/demo/SKILL.md":  &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: Demo skill " + marker + ".\n---\n\n# Demo\n")},
		"principles/example.md": &fstest.MapFile{Data: []byte("---\nid: example\ntitle: Example\ndescription: Example principle.\n---\n\n# Example\n\nDo the thing.\n")},
	}
}
