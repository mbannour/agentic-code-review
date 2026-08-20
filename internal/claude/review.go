package claude

import (
	"context"
	"fmt"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
)

// Reviewer is the boundary between Claude transport and this application's domain
// model.
//
// It owns exactly one journey: a selected context becomes review input, the input
// is handed to the CLI, and the CLI's answer becomes a validated
// findings.ReviewResult or an error. Nothing partially valid escapes — a result
// that breaks a rule is rejected here rather than being cleaned up later — and
// nothing here decides what will be published. That decision belongs to a later
// stage.
type Reviewer struct {
	client    *Client
	builder   InputBuilder
	validator findings.Validator
}

// NewReviewer returns a Reviewer using client for execution. A nil client means
// the default one.
func NewReviewer(client *Client) Reviewer {
	if client == nil {
		client = NewClient()
	}

	return Reviewer{
		client:    client,
		builder:   NewInputBuilder(),
		validator: findings.NewValidator(),
	}
}

// Outcome is one completed review: the validated result plus the transport detail
// worth reporting.
type Outcome struct {
	// Result is validated. Every finding in it satisfies every rule in
	// internal/findings, including the changed-file restriction.
	Result findings.ReviewResult

	// InputBytes is how much review input was sent.
	InputBytes int

	// Transport carries timing and diagnostics from the invocation. Its raw
	// output is never printed by default.
	Transport Result
}

// Available reports whether the Claude Code executable can be found.
func (r Reviewer) Available(ctx context.Context) error { return r.client.Available(ctx) }

// Review runs one review over the selected context and returns a validated
// result.
//
// repoDir, when set, is the local checkout the CLI runs in. The selected context
// remains the primary input either way, and the review stays read-only: no
// comment is posted, nothing is committed, and no file is written.
func (r Reviewer) Review(
	ctx context.Context,
	selected contextselect.SelectedContext,
	repoDir string,
) (Outcome, error) {
	input, err := r.builder.Build(selected)
	if err != nil {
		return Outcome{}, fmt.Errorf("build review input: %w", err)
	}

	transport, err := r.client.Review(ctx, Request{
		Input:            input.Content,
		WorkingDirectory: repoDir,
	})

	outcome := Outcome{InputBytes: input.Bytes(), Transport: transport}
	if err != nil {
		return outcome, err
	}

	// Strict decoding: unknown fields and stray prose are errors, so drift in what
	// the model emits is visible instead of silently discarded.
	result, err := findings.Decode(transport.Output)
	if err != nil {
		return outcome, fmt.Errorf("parse review result: %w", err)
	}

	if err := r.validator.Validate(result, selected); err != nil {
		return outcome, fmt.Errorf("validate review result: %w", err)
	}

	outcome.Result = result
	return outcome, nil
}
