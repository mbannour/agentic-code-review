package evidence

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileConnector reads exactly one explicitly configured file beneath the
// configuration directory. Symlinks are resolved before the containment check
// so a repository-controlled link cannot escape to a credential file.
type FileConnector struct {
	config SourceConfig
	root   string
	path   string
}

func NewFileConnector(config SourceConfig, root string) (*FileConnector, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence root symlinks: %w", err)
	}

	if filepath.IsAbs(config.Path) || escapesRoot(config.Path) {
		return nil, fmt.Errorf("file %s must stay beneath the evidence configuration directory", config.Path)
	}

	return &FileConnector{
		config: config, root: rootResolved,
		path: filepath.Join(rootResolved, filepath.Clean(config.Path)),
	}, nil
}

func (c *FileConnector) ID() string       { return c.config.ID }
func (c *FileConnector) Type() SourceType { return SourceFile }
func (c *FileConnector) Kind() Kind       { return c.config.Kind }
func (c *FileConnector) Required() bool   { return c.config.Required }

func (c *FileConnector) Collect(ctx context.Context) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}

	resolved, err := filepath.EvalSymlinks(c.path)
	if err != nil {
		return Document{}, fmt.Errorf("resolve configured file: %w", err)
	}
	if !withinRoot(c.root, resolved) {
		return Document{}, fmt.Errorf("configured file resolves outside the evidence configuration directory")
	}

	file, err := os.Open(resolved)
	if err != nil {
		return Document{}, fmt.Errorf("open configured file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Document{}, fmt.Errorf("inspect configured file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Document{}, fmt.Errorf("configured path is not a regular file")
	}

	raw, err := io.ReadAll(io.LimitReader(file, MaxRawSourceBytes+1))
	if err != nil {
		return Document{}, fmt.Errorf("read configured file: %w", err)
	}
	if len(raw) > MaxRawSourceBytes {
		return Document{}, fmt.Errorf("configured file exceeds the %d byte source limit", MaxRawSourceBytes)
	}

	original := string(raw)
	content, truncated := normalizeDocumentContent(original)
	return Document{
		ID: c.ID(), Kind: c.Kind(), SourceType: c.Type(),
		Locator: c.config.Path, Title: filepath.Base(resolved),
		Content: content, Digest: digest(original), Truncated: truncated,
	}, nil
}

func withinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !escapesRoot(rel)
}
