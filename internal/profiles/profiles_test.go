package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundledPlannerAndCodingProfiles(t *testing.T) {
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", t.TempDir())
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", t.TempDir())
	planner, err := Load("planner")
	if err != nil {
		t.Fatal(err)
	}
	coding, err := Load("coding")
	if err != nil {
		t.Fatal(err)
	}
	if len(planner.PrincipleIDs) != 8 || len(coding.PrincipleIDs) != 10 {
		t.Fatalf("planner=%#v coding=%#v", planner.PrincipleIDs, coding.PrincipleIDs)
	}
	for _, bundle := range []Bundle{planner, coding} {
		for _, expected := range []string{"WORKBENCH AGENT PROFILE", "Pause and re-evaluate", "Decision changed", "sha256:"} {
			if expected == "sha256:" {
				if !strings.HasPrefix(bundle.Digest, expected) {
					t.Fatalf("digest = %q", bundle.Digest)
				}
			} else if !strings.Contains(bundle.Text, expected) {
				t.Fatalf("%s missing %q", bundle.ID, expected)
			}
		}
		if !strings.HasPrefix(bundle.Source, "embedded:") {
			t.Fatalf("source = %q", bundle.Source)
		}
	}
}

func TestInstalledProfileOverridesBundleAndRejectsInvalidContent(t *testing.T) {
	profilesRoot := t.TempDir()
	principlesRoot := t.TempDir()
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", profilesRoot)
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", principlesRoot)
	writePrinciple(t, principlesRoot, "one")
	writePrinciple(t, principlesRoot, "two")
	path := filepath.Join(profilesRoot, "planner.md")
	if err := os.WriteFile(path, []byte("---\nid: planner\nprinciples:\n  - two\n  - one\n---\nCustom planner guidance.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load("planner")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(bundle.PrincipleIDs, ",") != "two,one" || !strings.Contains(bundle.Text, "Custom planner guidance") || bundle.Source != path {
		t.Fatalf("bundle = %#v", bundle)
	}
	if err := os.WriteFile(path, []byte("---\nid: wrong\nprinciples:\n  - one\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("planner"); err == nil || !strings.Contains(err.Error(), "must match filename") {
		t.Fatalf("err = %v", err)
	}
}

func TestProfileRejectsDuplicateAndUnknownPrinciples(t *testing.T) {
	profilesRoot := t.TempDir()
	principlesRoot := t.TempDir()
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", profilesRoot)
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", principlesRoot)
	writePrinciple(t, principlesRoot, "one")
	path := filepath.Join(profilesRoot, "custom.md")
	if err := os.WriteFile(path, []byte("---\nid: custom\nprinciples:\n  - one\n  - one\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("custom"); err == nil || !strings.Contains(err.Error(), "repeats principle") {
		t.Fatalf("duplicate err = %v", err)
	}
	if err := os.WriteFile(path, []byte("---\nid: custom\nprinciples:\n  - missing\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("custom"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing err = %v", err)
	}
}

func writePrinciple(t *testing.T, root, id string) {
	t.Helper()
	content := "---\nid: " + id + "\ntitle: " + strings.ToUpper(id) + "\ndescription: Test principle.\n---\n# Test\n\n## Use when\n\nTesting.\n\n## Rule\n\nDo it.\n\n## Decision changed\n\nBefore → after.\n\n## Proof\n\nIt worked.\n"
	if err := os.WriteFile(filepath.Join(root, id+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
