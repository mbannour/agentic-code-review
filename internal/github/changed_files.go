package github

import (
	"context"
	"fmt"
	"net/url"
)

const (
	// filesPerPage is the page size used when listing changed files. 100 is the
	// maximum GitHub allows, which keeps the number of round trips down.
	filesPerPage = 100

	// maxFilePages bounds pagination so a misbehaving API cannot loop forever.
	// At 100 files per page this covers 3000 changed files.
	maxFilePages = 30
)

// ChangedFile is one file touched by a pull request.
type ChangedFile struct {
	Filename  string
	Status    string
	Additions int
	Deletions int
	Changes   int

	// Patch is the unified diff hunk for this file. GitHub omits it for binary
	// files and for very large diffs, in which case it is the empty string.
	Patch string
}

// HasPatch reports whether GitHub returned a diff for this file.
func (f ChangedFile) HasPatch() bool { return f.Patch != "" }

// changedFileResponse mirrors the wire format, keeping GitHub's JSON shape out
// of the domain type.
type changedFileResponse struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`

	// Patch is absent for binary and oversized files; a missing key decodes to
	// the zero value, which is exactly the empty patch we want.
	Patch string `json:"patch"`
}

func (r changedFileResponse) toChangedFile() ChangedFile {
	return ChangedFile{
		Filename:  r.Filename,
		Status:    r.Status,
		Additions: r.Additions,
		Deletions: r.Deletions,
		Changes:   r.Changes,
		Patch:     r.Patch,
	}
}

// ChangeSummary aggregates the size of a pull request's changes.
type ChangeSummary struct {
	Files     int
	Additions int
	Deletions int
}

// SummarizeChanges totals the files, additions, and deletions in files.
func SummarizeChanges(files []ChangedFile) ChangeSummary {
	summary := ChangeSummary{Files: len(files)}
	for _, f := range files {
		summary.Additions += f.Additions
		summary.Deletions += f.Deletions
	}
	return summary
}

// GetPullRequestFiles lists every file changed by pr via
// GET /repos/{owner}/{repo}/pulls/{pull_number}/files.
//
// It uses the batch endpoint — one request per page of 100 files, never one
// request per file — and follows pages until a short or empty page arrives.
func (c *Client) GetPullRequestFiles(ctx context.Context, pr PullRequest) ([]ChangedFile, error) {
	var files []ChangedFile

	for page := 1; page <= maxFilePages; page++ {
		query := url.Values{
			"per_page": {fmt.Sprint(filesPerPage)},
			"page":     {fmt.Sprint(page)},
		}

		var body []changedFileResponse
		description := fmt.Sprintf("pull request files %s (page %d)", pr, page)
		if err := c.getJSON(ctx, pullsPath(pr)+"/files", query, &body, description); err != nil {
			return nil, err
		}

		for _, item := range body {
			files = append(files, item.toChangedFile())
		}

		// A short page — including an empty one — is the last page.
		if len(body) < filesPerPage {
			return files, nil
		}
	}

	return nil, fmt.Errorf("pull request files %s: exceeded %d pages of %d files", pr, maxFilePages, filesPerPage)
}
