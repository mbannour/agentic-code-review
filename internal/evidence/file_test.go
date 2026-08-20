package evidence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileConnectorReadsOnlyConfiguredFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "requirements"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "requirements", "customer.md")
	if err := os.WriteFile(path, []byte("Orders must retain the customer reference."), 0o600); err != nil {
		t.Fatal(err)
	}

	connector, err := NewFileConnector(SourceConfig{
		ID: "customer", Type: SourceFile, Kind: KindRequirement,
		Path: "requirements/customer.md", Required: true,
	}, root)
	if err != nil {
		t.Fatalf("NewFileConnector() = %v", err)
	}
	document, err := connector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if document.Content != "Orders must retain the customer reference." ||
		document.Locator != "requirements/customer.md" || !strings.HasPrefix(document.Digest, "sha256:") {
		t.Fatalf("document = %+v", document)
	}
}

func TestFileConnectorRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "requirements.md")); err != nil {
		t.Fatal(err)
	}

	connector, err := NewFileConnector(SourceConfig{
		ID: "customer", Type: SourceFile, Kind: KindRequirement, Path: "requirements.md",
	}, root)
	if err != nil {
		t.Fatalf("NewFileConnector() = %v", err)
	}
	_, err = connector.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Collect() = %v, want containment error", err)
	}
}
