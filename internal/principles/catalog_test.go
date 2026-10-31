package principles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogListsLoadsAndRendersInRequestedOrder(t *testing.T) {
	root := t.TempDir()
	writePrinciple(t, root, "alpha", "Alpha", "First rule.", "# Alpha\n\nDo alpha.\n")
	writePrinciple(t, root, "beta", "Beta", "Second rule.", "# Beta\n\nDo beta.\n")
	catalog := New(root)
	listed, err := catalog.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != "alpha" || listed[1].ID != "beta" {
		t.Fatalf("listed = %#v", listed)
	}
	bundle, err := catalog.Render([]string{"beta", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(bundle.Text, "Do beta") > strings.Index(bundle.Text, "Do alpha") {
		t.Fatalf("render did not preserve requested order:\n%s", bundle.Text)
	}
	for _, expected := range []string{"Pause and re-evaluate", "state the concrete decision it changed", "continue the user's actual task", "sha256:"} {
		if expected == "sha256:" {
			if !strings.HasPrefix(bundle.Digest, expected) {
				t.Fatalf("digest = %q", bundle.Digest)
			}
		} else if !strings.Contains(bundle.Text, expected) {
			t.Fatalf("render missing %q:\n%s", expected, bundle.Text)
		}
	}
	second, err := catalog.Render([]string{"beta", "alpha"})
	if err != nil || second.Digest != bundle.Digest {
		t.Fatalf("render not deterministic: %#v, %v", second, err)
	}
}

func TestCatalogPreservesCRLFBody(t *testing.T) {
	root := t.TempDir()
	content := "---\r\nid: alpha\r\ntitle: Alpha\r\ndescription: First rule.\r\n---\r\n# Alpha\r\n\r\nDo alpha.\r\n"
	if err := os.WriteFile(filepath.Join(root, "alpha.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := New(root).Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if item.Body != "# Alpha\r\n\r\nDo alpha.\r\n" {
		t.Fatalf("body = %q", item.Body)
	}
}

func TestCatalogRejectsInvalidPrinciples(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content string
		want    string
	}{
		{name: "missing frontmatter", file: "alpha.md", content: "# Alpha\n", want: "missing YAML frontmatter"},
		{name: "metadata mismatch", file: "alpha.md", content: principleFile("beta", "Beta", "desc", "body"), want: "must match filename"},
		{name: "empty body", file: "alpha.md", content: principleFile("alpha", "Alpha", "desc", "  \n"), want: "body is empty"},
		{name: "missing description", file: "alpha.md", content: "---\nid: alpha\ntitle: Alpha\n---\nbody\n", want: "requires string id, title, and description"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, test.file), []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := New(root).List()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCatalogRejectsSymlinkAndOversizedFile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte(principleFile("linked", "Linked", "desc", "body")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Load("linked"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink err = %v", err)
	}
	oversized := principleFile("large", "Large", "desc", strings.Repeat("x", MaxFileBytes))
	if err := os.WriteFile(filepath.Join(root, "large.md"), []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Load("large"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized err = %v", err)
	}
}

func TestRenderRejectsMissingAndDuplicateIDs(t *testing.T) {
	root := t.TempDir()
	writePrinciple(t, root, "alpha", "Alpha", "desc", "body\n")
	catalog := New(root)
	if _, err := catalog.Render(nil); err == nil {
		t.Fatal("empty render succeeded")
	}
	if _, err := catalog.Render([]string{"missing"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := catalog.Render([]string{"alpha", "alpha"}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate err = %v", err)
	}
}

func writePrinciple(t *testing.T, root, id, title, description, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, id+".md"), []byte(principleFile(id, title, description, body)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func principleFile(id, title, description, body string) string {
	return "---\nid: " + id + "\ntitle: " + title + "\ndescription: " + description + "\n---\n" + body
}
