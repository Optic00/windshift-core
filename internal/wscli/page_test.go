package wscli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePageInput_TitleFlagWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte("# Heading\n\nbody"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	title, content, err := resolvePageInput("Explicit", "", path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if title != "Explicit" {
		t.Errorf("title: want Explicit, got %q", title)
	}
	if content != "# Heading\n\nbody" {
		t.Errorf("content not passed through: %q", content)
	}
}

func TestResolvePageInput_FallsBackToFirstH1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte("# Heading\n\nbody"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	title, _, err := resolvePageInput("", "", path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if title != "Heading" {
		t.Errorf("title: want Heading, got %q", title)
	}
}

func TestResolvePageInput_FallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "onboarding-guide.md")
	if err := os.WriteFile(path, []byte("no heading here"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	title, _, err := resolvePageInput("", "", path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if title != "onboarding-guide" {
		t.Errorf("title: want onboarding-guide, got %q", title)
	}
}

func TestResolvePageInput_InlineContent(t *testing.T) {
	title, content, err := resolvePageInput("Notes", "Initial body", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if title != "Notes" {
		t.Errorf("title: want Notes, got %q", title)
	}
	if content != "Initial body" {
		t.Errorf("content: want Initial body, got %q", content)
	}
}

func TestResolvePageInput_NoInput(t *testing.T) {
	title, content, err := resolvePageInput("", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if title != "" || content != "" {
		t.Errorf("expected empty (title=%q, content=%q)", title, content)
	}
}

func TestResolvePageInput_H1RegexSkipsLowerHeadings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	// First heading is H2 — must NOT be picked up as the title.
	body := "## Subsection\n\n## Another\n\n# Real Title\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	title, _, err := resolvePageInput("", "", path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if title != "Real Title" {
		t.Errorf("title: want 'Real Title', got %q", title)
	}
}
