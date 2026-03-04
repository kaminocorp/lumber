package lumber

import (
	"testing"

	"github.com/kaminocorp/lumber/internal/engine/taxonomy"
)

func TestTaxonomyReturnsAllRoots(t *testing.T) {
	// Use DefaultRoots directly to build expected count
	// without needing the full ONNX model.
	roots := taxonomy.DefaultRoots()
	expectedRoots := len(roots)

	var totalLeaves int
	for _, root := range roots {
		totalLeaves += len(root.Children)
	}

	// Build a Lumber with taxonomy for introspection.
	// We can't use New() without ONNX, so test the Taxonomy() method
	// by constructing a Taxonomy directly using a nil embedder test.
	// Instead, verify the taxonomy structure via DefaultRoots.
	if expectedRoots != 8 {
		t.Errorf("expected 8 root categories, got %d", expectedRoots)
	}
	if totalLeaves != 42 {
		t.Errorf("expected 42 leaf labels, got %d", totalLeaves)
	}
}

func TestTaxonomyIntrospection(t *testing.T) {
	skipWithoutModel(t)

	l, err := New(WithModelDir(testModelDir))
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer l.Close()

	categories := l.Taxonomy()

	if len(categories) != 8 {
		t.Fatalf("got %d categories, want 8", len(categories))
	}

	var totalLabels int
	for _, cat := range categories {
		totalLabels += len(cat.Labels)
	}
	if totalLabels != 42 {
		t.Fatalf("got %d total labels, want 42", totalLabels)
	}

	// Verify ERROR root has expected structure.
	var errorCat *Category
	for i := range categories {
		if categories[i].Name == "ERROR" {
			errorCat = &categories[i]
			break
		}
	}
	if errorCat == nil {
		t.Fatal("ERROR category not found")
	}

	// Check a known label.
	found := false
	for _, label := range errorCat.Labels {
		if label.Name == "connection_failure" {
			found = true
			if label.Path != "ERROR.connection_failure" {
				t.Errorf("Path = %q, want ERROR.connection_failure", label.Path)
			}
			if label.Severity != "error" {
				t.Errorf("Severity = %q, want error", label.Severity)
			}
		}
	}
	if !found {
		t.Error("connection_failure label not found in ERROR category")
	}
}

func TestTaxonomyPathFormat(t *testing.T) {
	skipWithoutModel(t)

	l, err := New(WithModelDir(testModelDir))
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer l.Close()

	for _, cat := range l.Taxonomy() {
		for _, label := range cat.Labels {
			expected := cat.Name + "." + label.Name
			if label.Path != expected {
				t.Errorf("label %q: Path = %q, want %q", label.Name, label.Path, expected)
			}
		}
	}
}
