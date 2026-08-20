package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrUnsupportedContent is returned when the contents endpoint answers with
// something this client cannot turn into text: a directory, a submodule, or a
// blob too large for the contents API to inline.
var ErrUnsupportedContent = errors.New("unsupported repository content")

// contentsResponse mirrors the wire format of the repository contents endpoint
// for a single file.
type contentsResponse struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// GetRepositoryFile reads one file from a repository at ref via
// GET /repos/{owner}/{repo}/contents/{path}?ref={ref}.
//
// ref should be a commit SHA so the content matches an exact snapshot. Base64
// decoding happens here: callers receive plain text.
//
// A missing file returns an error matching ErrNotFound, which lets callers treat
// optional files as absent without ignoring real failures.
func (c *Client) GetRepositoryFile(ctx context.Context, owner string, repo string, path string, ref string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("github: empty repository file path")
	}

	query := url.Values{}
	if ref != "" {
		query.Set("ref", ref)
	}

	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s",
		url.PathEscape(owner),
		url.PathEscape(repo),
		escapePath(path),
	)

	var body contentsResponse
	description := fmt.Sprintf("repository file %s/%s:%s at %s", owner, repo, path, refLabel(ref))
	if err := c.getJSON(ctx, endpoint, query, &body, description); err != nil {
		return "", err
	}

	if body.Type != "" && body.Type != "file" {
		return "", fmt.Errorf("%w: %s is a %s, not a file", ErrUnsupportedContent, path, body.Type)
	}

	switch body.Encoding {
	case "base64":
		// GitHub wraps the payload at 60 characters; the decoder rejects the
		// newlines unless they are stripped first.
		cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(body.Content)
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", fmt.Errorf("decode base64 content of %s: %w", path, err)
		}
		return string(decoded), nil

	case "", "none":
		// "none" means the blob is too large for the contents API to inline.
		if body.Content == "" && body.Encoding == "none" {
			return "", fmt.Errorf("%w: %s is too large to read through the contents API", ErrUnsupportedContent, path)
		}
		// An absent encoding with inline content: take it as plain text.
		return body.Content, nil

	case "utf-8":
		return body.Content, nil

	default:
		return "", fmt.Errorf("%w: %s uses unknown encoding %q", ErrUnsupportedContent, path, body.Encoding)
	}
}

// escapePath escapes each path segment while keeping the separators intact, so
// "a dir/file name.md" becomes "a%20dir/file%20name.md".
func escapePath(path string) string {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// refLabel describes the ref for error messages.
func refLabel(ref string) string {
	if ref == "" {
		return "the default branch"
	}
	return ref
}
