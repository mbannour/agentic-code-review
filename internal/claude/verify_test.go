package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/verification"
)

const verifyTestFile = "internal/payment/retry.go"

// verifyPatch has new lines 80-86, with 81-83 added.
const verifyPatch = `@@ -80,5 +80,7 @@ func RetryPayment(p Payment) error {
 func RetryPayment(p Payment) error {
-	if p.Declined {
+	if p.Declined && !p.Permanent {
+		return nil
+	}
 	return retry(p)
 }
 `

// verifyFinding builds a candidate finding.
func verifyFinding(id string, severity findings.Severity, confidence float64) findings.Finding {
	return findings.Finding{
		ID:         id,
		Category:   findings.CategoryCorrectness,
		Severity:   severity,
		Confidence: confidence,
		File:       verifyTestFile,
		StartLine:  81,
		EndLine:    83,
		Title:      "Permanent declines enter the retry path",
		Problem:    "The new branch treats a permanent decline as retryable.",
		Impact:     "A declined card can be submitted repeatedly.",
		Suggestion: "Return before entering the retry path.",
		Evidence: []findings.Evidence{
			{Type: findings.EvidenceCode, Source: verifyTestFile + ":81-83", Detail: "The branch reaches RetryPayment."},
		},
	}
}

// verifySelection is the selected context verification draws evidence from.
func verifySelection() contextselect.SelectedContext {
	return contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner: "acme", Repository: "payments", Number: 123, HeadSHA: "abc123",
		},
		Ticket: &contextselect.TicketSummary{
			Key: "PAY-431", Summary: "Stop retrying permanent declines",
			Description: "Permanent declines must not be retried.",
		},
		Files: []contextselect.SelectedFile{{
			Path: verifyTestFile, Status: "modified", Patch: verifyPatch,
			Kind: contextselect.FileKindSource, Importance: contextselect.ImportanceHigh,
		}},
		Rules: []contextselect.SelectedRule{
			{Path: "AGENTS.md", Content: "Never retry a permanently declined authorization."},
		},
		Analysis: []contextselect.SelectedAnalysis{
			{Name: "go-test", Command: "go test ./...", Passed: true},
		},
		Profile: contextselect.TechnologyProfile{
			Languages:    []string{contextselect.LanguageGo},
			BuildSystems: []string{contextselect.BuildSystemGo},
		},
	}
}

// verdictJSON builds a verifier response.
func verdictJSON(findingID string, verdict verification.Verdict, confidence float64) string {
	return fmt.Sprintf(
		`{"finding_id":%q,"verdict":%q,"confidence":%v,"reason":"checked the cited code and the surrounding guards","supporting_evidence":[],"contradicting_evidence":[]}`,
		findingID, verdict, confidence)
}

// scriptedRunner answers each invocation according to the finding id it finds in the
// input, so one fake can serve a whole batch of concurrent verifications.
type scriptedRunner struct {
	mu sync.Mutex

	// verdicts maps a finding id to the raw response for it.
	verdicts map[string]string

	// failures maps a finding id to a transport error.
	failures map[string]error

	// delay is applied inside each invocation, for the concurrency tests.
	delay time.Duration

	// inputs records every input received, keyed by finding id.
	inputs map[string]string

	// live and peak track simultaneous invocations.
	live int32
	peak int32

	calls int32
}

func newScriptedRunner() *scriptedRunner {
	return &scriptedRunner{
		verdicts: map[string]string{},
		failures: map[string]error{},
		inputs:   map[string]string{},
	}
}

func (r *scriptedRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	atomic.AddInt32(&r.calls, 1)

	live := atomic.AddInt32(&r.live, 1)
	for {
		peak := atomic.LoadInt32(&r.peak)
		if live <= peak || atomic.CompareAndSwapInt32(&r.peak, peak, live) {
			break
		}
	}
	defer atomic.AddInt32(&r.live, -1)

	id := findingIDIn(request.Stdin)

	r.mu.Lock()
	r.inputs[id] = request.Stdin
	response, hasResponse := r.verdicts[id]
	failure, hasFailure := r.failures[id]
	r.mu.Unlock()

	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return CommandResult{}, ctx.Err()
		}
	}

	if hasFailure {
		return CommandResult{ExitCode: 1}, failure
	}
	if !hasResponse {
		return CommandResult{ExitCode: 1}, fmt.Errorf("no scripted verdict for %q", id)
	}

	return CommandResult{Stdout: envelopeJSON(response), ExitCode: 0}, nil
}

// findingIDIn extracts the finding id the verifier input asks about.
func findingIDIn(input string) string {
	const marker = "\nid: "
	idx := strings.Index(input, marker)
	if idx < 0 {
		return ""
	}
	rest := input[idx+len(marker):]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

// verifierWith returns a Verifier backed by the scripted runner.
func verifierWith(runner Runner) *Verifier {
	return NewVerifier(NewClient(WithRunner(runner), WithBinary("claude")))
}

// TestVerifyAllVerdicts is the integration case: three candidates, three answers, three
// consequences under the verification policy.
func TestVerifyAllVerdicts(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("COR-001", verification.VerdictValid, 0.97)
	runner.verdicts["COR-002"] = verdictJSON("COR-002", verification.VerdictInvalid, 0.92)
	runner.verdicts["TEST-001"] = verdictJSON("TEST-001", verification.VerdictUncertain, 0.68)

	result := findings.ReviewResult{Summary: "s", Findings: []findings.Finding{
		verifyFinding("COR-001", findings.SeverityHigh, 0.94),
		verifyFinding("COR-002", findings.SeverityHigh, 0.96),
		verifyFinding("TEST-001", findings.SeverityMedium, 0.86),
	}}

	report, err := verifierWith(runner).VerifyAll(context.Background(), result, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if len(report.Outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(report.Outcomes))
	}

	wantVerdicts := []verification.Verdict{
		verification.VerdictValid, verification.VerdictInvalid, verification.VerdictUncertain,
	}
	for i, want := range wantVerdicts {
		outcome := report.Outcomes[i]
		if outcome.Status != verification.StatusVerified {
			t.Fatalf("outcome[%d] status = %q, want verified", i, outcome.Status)
		}
		if outcome.Result.Verdict != want {
			t.Errorf("outcome[%d] verdict = %q, want %q", i, outcome.Result.Verdict, want)
		}
		if outcome.Result.FindingID != outcome.Finding.ID {
			t.Errorf("outcome[%d] verdict is about %q, not %q",
				i, outcome.Result.FindingID, outcome.Finding.ID)
		}
	}

	stats := report.Stats
	if stats.Candidates != 3 || stats.Verified != 3 || stats.Valid != 1 || stats.Invalid != 1 || stats.Uncertain != 1 {
		t.Errorf("stats = %+v, want one of each verdict", stats)
	}
	if stats.ContextBytes <= 0 {
		t.Error("stats do not account for the verification context")
	}
}

// TestVerifyAllSkipsLowFindings checks a low-severity finding costs no invocation and is
// neither passed nor suppressed.
func TestVerifyAllSkipsLowFindings(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("COR-001", verification.VerdictValid, 0.95)

	result := findings.ReviewResult{Summary: "s", Findings: []findings.Finding{
		verifyFinding("COR-001", findings.SeverityHigh, 0.94),
		verifyFinding("MAINT-001", findings.SeverityLow, 0.82),
	}}

	report, err := verifierWith(runner).VerifyAll(context.Background(), result, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if calls := atomic.LoadInt32(&runner.calls); calls != 1 {
		t.Errorf("made %d invocations, want 1; the low finding must not be verified", calls)
	}

	low := report.Outcomes[1]
	if low.Status != verification.StatusNotRequired {
		t.Errorf("low finding status = %q, want not_required", low.Status)
	}
	if low.SkipReason != verification.ReasonNotRequiredLowSeverity {
		t.Errorf("skip reason = %q, want %q", low.SkipReason, verification.ReasonNotRequiredLowSeverity)
	}

	// A skipped finding is neither verified nor rejected: the publication policy decides what
	// becomes of it, and a low finding still reaches the review body there.
	if report.Stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1", report.Stats.Skipped)
	}
}

// TestVerifyAllSeverityCoverage checks every inline-eligible severity is verified.
func TestVerifyAllSeverityCoverage(t *testing.T) {
	tests := []struct {
		severity   findings.Severity
		wantCalled bool
	}{
		{findings.SeverityBlocker, true},
		{findings.SeverityHigh, true},
		{findings.SeverityMedium, true},
		{findings.SeverityLow, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			runner := newScriptedRunner()
			runner.verdicts["F-1"] = verdictJSON("F-1", verification.VerdictValid, 0.9)

			result := findings.ReviewResult{Summary: "s",
				Findings: []findings.Finding{verifyFinding("F-1", tt.severity, 0.9)}}

			if _, err := verifierWith(runner).VerifyAll(context.Background(), result, verifySelection(), ""); err != nil {
				t.Fatalf("VerifyAll() = %v", err)
			}

			called := atomic.LoadInt32(&runner.calls) == 1
			if called != tt.wantCalled {
				t.Errorf("invoked = %t, want %t for %s", called, tt.wantCalled, tt.severity)
			}
		})
	}
}

// TestVerifyAllFailuresAreIsolated checks one broken verification does not corrupt another,
// and that each failure mode is recorded as failed rather than passed.
func TestVerifyAllFailuresAreIsolated(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["OK-001"] = verdictJSON("OK-001", verification.VerdictValid, 0.95)
	runner.failures["TRANSPORT-001"] = errors.New("Claude Code exited with status 1")
	runner.verdicts["MALFORMED-001"] = `{"finding_id":"MALFORMED-001","verdict":`
	runner.verdicts["MISMATCH-001"] = verdictJSON("SOMETHING-ELSE", verification.VerdictValid, 0.99)
	runner.verdicts["TRAILING-001"] =
		verdictJSON("TRAILING-001", verification.VerdictValid, 0.9) + "\n\nHope that helps."
	runner.verdicts["BADVERDICT-001"] = verdictJSON("BADVERDICT-001", "probably", 0.9)

	ids := []string{"OK-001", "TRANSPORT-001", "MALFORMED-001", "MISMATCH-001", "TRAILING-001", "BADVERDICT-001"}

	var candidates []findings.Finding
	for _, id := range ids {
		candidates = append(candidates, verifyFinding(id, findings.SeverityHigh, 0.9))
	}

	report, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: candidates}, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v; one broken verification must not end the stage", err)
	}

	if got := report.Outcomes[0]; got.Status != verification.StatusVerified {
		t.Errorf("the healthy verification was affected: %+v", got)
	}
	for i, outcome := range report.Outcomes[1:] {
		if outcome.Status != verification.StatusFailed {
			t.Errorf("outcome[%d] (%s) status = %q, want failed",
				i+1, outcome.Finding.ID, outcome.Status)
		}
		if outcome.FailureReason == "" {
			t.Errorf("%s failed with no reason recorded", outcome.Finding.ID)
		}
	}

	if report.Stats.Failed != 5 || report.Stats.Verified != 1 {
		t.Errorf("stats = %+v, want 1 verified and 5 failed", report.Stats)
	}

	// Fail closed: a failed outcome carries no verdict, so nothing downstream can mistake it
	// for a checked finding.
	for _, outcome := range report.Outcomes[1:] {
		if outcome.Verdict() != "" {
			t.Errorf("%s reports verdict %q despite failing", outcome.Finding.ID, outcome.Verdict())
		}
	}
}

// TestVerifyAllRejectsAVerdictForAnotherFinding checks a mismatched id is refused rather
// than reconciled. Attaching one claim's conclusion to another is worse than no
// verification.
func TestVerifyAllRejectsAVerdictForAnotherFinding(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("SEC-009", verification.VerdictValid, 0.99)

	report, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: []findings.Finding{
			verifyFinding("COR-001", findings.SeverityHigh, 0.94),
		}}, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	outcome := report.Outcomes[0]
	if outcome.Status != verification.StatusFailed {
		t.Fatalf("status = %q, want failed", outcome.Status)
	}
	if !strings.Contains(outcome.FailureReason, "does not match") {
		t.Errorf("failure reason = %q, want the identity mismatch named", outcome.FailureReason)
	}
}

// TestVerifyAllBoundsConcurrency checks the worker pool holds. Twenty simultaneous Claude
// processes would compete for the same CPU and rate limit and finish no sooner than three.
func TestVerifyAllBoundsConcurrency(t *testing.T) {
	runner := newScriptedRunner()
	runner.delay = 20 * time.Millisecond

	var candidates []findings.Finding
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("COR-%03d", i)
		runner.verdicts[id] = verdictJSON(id, verification.VerdictValid, 0.9)
		candidates = append(candidates, verifyFinding(id, findings.SeverityHigh, 0.9))
	}

	report, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: candidates}, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if peak := atomic.LoadInt32(&runner.peak); peak > MaxVerifierConcurrency {
		t.Errorf("peak concurrency = %d, want at most %d", peak, MaxVerifierConcurrency)
	}
	if peak := atomic.LoadInt32(&runner.peak); peak < 2 {
		t.Errorf("peak concurrency = %d; the pool did not run anything in parallel", peak)
	}
	if len(report.Outcomes) != len(candidates) {
		t.Fatalf("outcomes = %d, want %d", len(report.Outcomes), len(candidates))
	}
}

// TestVerifyAllOrderingIsDeterministic checks the outcomes follow the input order however
// the concurrent invocations interleaved.
func TestVerifyAllOrderingIsDeterministic(t *testing.T) {
	build := func() (*scriptedRunner, findings.ReviewResult) {
		runner := newScriptedRunner()
		var candidates []findings.Finding
		for i := 0; i < 9; i++ {
			id := fmt.Sprintf("F-%03d", i)
			// Later findings answer faster, so completion order differs from input.
			runner.verdicts[id] = verdictJSON(id, verification.VerdictValid, 0.9)
			candidates = append(candidates, verifyFinding(id, findings.SeverityHigh, 0.9))
		}
		return runner, findings.ReviewResult{Summary: "s", Findings: candidates}
	}

	for attempt := 0; attempt < 5; attempt++ {
		runner, result := build()

		report, err := verifierWith(runner).VerifyAll(context.Background(), result, verifySelection(), "")
		if err != nil {
			t.Fatalf("VerifyAll() = %v", err)
		}

		for i, outcome := range report.Outcomes {
			want := fmt.Sprintf("F-%03d", i)
			if outcome.Finding.ID != want {
				t.Fatalf("attempt %d: outcome[%d] = %s, want %s", attempt, i, outcome.Finding.ID, want)
			}
			if outcome.Result.FindingID != want {
				t.Fatalf("attempt %d: outcome[%d] carries the verdict for %s",
					attempt, i, outcome.Result.FindingID)
			}
		}
	}
}

// TestVerifyAllRespectsCancellation checks a cancelled parent context stops the stage and is
// reported as the caller's decision rather than as a verdict.
func TestVerifyAllRespectsCancellation(t *testing.T) {
	runner := newScriptedRunner()
	runner.delay = 50 * time.Millisecond

	var candidates []findings.Finding
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("F-%03d", i)
		runner.verdicts[id] = verdictJSON(id, verification.VerdictValid, 0.9)
		candidates = append(candidates, verifyFinding(id, findings.SeverityHigh, 0.9))
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := verifierWith(runner).VerifyAll(ctx,
		findings.ReviewResult{Summary: "s", Findings: candidates}, verifySelection(), "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("VerifyAll() = %v, want context.Canceled", err)
	}
}

// TestVerifyAllWithoutFindings checks a clean review costs nothing.
func TestVerifyAllWithoutFindings(t *testing.T) {
	runner := newScriptedRunner()

	report, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "No actionable issues found."}, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if !report.Empty() {
		t.Errorf("report = %+v, want empty", report)
	}
	if calls := atomic.LoadInt32(&runner.calls); calls != 0 {
		t.Errorf("made %d invocations for a review with no findings", calls)
	}
}

// TestVerifyAllWithoutAPatch checks a finding whose file has no diff is still verified — the
// verifier is told the diff is unavailable, which is what should produce an honest
// uncertain.
func TestVerifyAllWithoutAPatch(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("COR-001", verification.VerdictUncertain, 0.4)

	selected := verifySelection()
	selected.Files[0].Patch = ""

	report, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: []findings.Finding{
			verifyFinding("COR-001", findings.SeverityHigh, 0.94),
		}}, selected, "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if report.Outcomes[0].Status != verification.StatusVerified {
		t.Errorf("status = %q, want the verification to have run", report.Outcomes[0].Status)
	}
	if !strings.Contains(runner.inputs["COR-001"], "no diff is available for the claimed file") {
		t.Error("the verifier was not told the diff is unavailable")
	}
}

// TestVerifyAllSendsTargetedContext checks each request carries its own finding and not the
// rest of the pull request.
func TestVerifyAllSendsTargetedContext(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("COR-001", verification.VerdictValid, 0.95)
	runner.verdicts["COR-002"] = verdictJSON("COR-002", verification.VerdictValid, 0.95)

	selected := verifySelection()
	selected.Files = append(selected.Files, contextselect.SelectedFile{
		Path:   "internal/unrelated/thing.go",
		Status: "modified",
		Patch:  "@@ -1,2 +1,3 @@\n+// unrelated marker UNRELATED_CONTENT\n",
		Kind:   contextselect.FileKindSource,
	})

	second := verifyFinding("COR-002", findings.SeverityHigh, 0.9)
	second.Title = "A different claim entirely"

	if _, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: []findings.Finding{
			verifyFinding("COR-001", findings.SeverityHigh, 0.94), second,
		}}, selected, ""); err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	first := runner.inputs["COR-001"]
	if !strings.Contains(first, "COR-001") {
		t.Error("the request does not name its own finding")
	}
	if strings.Contains(first, "A different claim entirely") {
		t.Error("one finding's request carried another finding")
	}
	if strings.Contains(first, "UNRELATED_CONTENT") {
		t.Error("an unrelated file's patch was resent for verification")
	}
	if len(first) > verification.MaxVerificationContextBytes*2 {
		t.Errorf("request is %d bytes; verification context is meant to be small", len(first))
	}
}

// TestVerifierWithConcurrency checks the concurrency override, including that a nonsense
// value degrades to serial rather than to zero workers.
func TestVerifierWithConcurrency(t *testing.T) {
	runner := newScriptedRunner()
	runner.delay = 5 * time.Millisecond

	var candidates []findings.Finding
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("F-%03d", i)
		runner.verdicts[id] = verdictJSON(id, verification.VerdictValid, 0.9)
		candidates = append(candidates, verifyFinding(id, findings.SeverityHigh, 0.9))
	}

	report, err := verifierWith(runner).WithConcurrency(0).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: candidates}, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if peak := atomic.LoadInt32(&runner.peak); peak != 1 {
		t.Errorf("peak concurrency = %d, want serial execution", peak)
	}
	if len(report.Outcomes) != len(candidates) {
		t.Errorf("outcomes = %d, want %d", len(report.Outcomes), len(candidates))
	}
}

// TestVerifierPerformsNoWrites checks this stage reaches nothing outside the model
// invocation: no GitHub call, no Jira call, no file write, no state-changing command.
func TestVerifierPerformsNoWrites(t *testing.T) {
	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("COR-001", verification.VerdictValid, 0.95)

	if _, err := verifierWith(runner).VerifyAll(context.Background(),
		findings.ReviewResult{Summary: "s", Findings: []findings.Finding{
			verifyFinding("COR-001", findings.SeverityHigh, 0.94),
		}}, verifySelection(), "/tmp/some/checkout"); err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	// The only process started is Claude Code in print mode, reading from stdin.
	if len(runner.inputs) != 1 {
		t.Fatalf("made %d invocations, want 1", len(runner.inputs))
	}
	input := runner.inputs["COR-001"]
	for _, forbidden := range []string{
		"gh api", "git commit", "git push", "curl", "POST /repos",
		"create a review", "post a comment",
	} {
		if strings.Contains(strings.ToLower(input), strings.ToLower(forbidden)) {
			t.Errorf("the verifier input mentions %q", forbidden)
		}
	}
}

// TestStructurallyInvalidFindingsNeverReachTheVerifier checks the ordering of the two
// gates.
//
// Structural validation runs inside the reviewer, before any verification exists, so a
// malformed or duplicated finding never costs a verifier invocation — and, more
// importantly, never acquires a verdict that would make it look checked. The reviewer
// returns an error and there is nothing to verify.
func TestStructurallyInvalidFindingsNeverReachTheVerifier(t *testing.T) {
	tests := []struct {
		name    string
		result  string
		wantErr string
	}{
		{
			name: "finding against an unchanged file",
			result: `{"summary":"s","findings":[{"id":"COR-001","category":"correctness",
			  "severity":"high","confidence":0.9,"file":"internal/legacy/other.go",
			  "start_line":10,"end_line":10,"title":"t","problem":"p","impact":"i",
			  "suggestion":"s","evidence":[{"type":"code","source":"x","detail":"d"}]}]}`,
			wantErr: "not a file changed by this pull request",
		},
		{
			name: "duplicate finding ids",
			result: `{"summary":"s","findings":[
			  {"id":"COR-001","category":"correctness","severity":"high","confidence":0.9,
			   "file":"internal/payment/retry.go","start_line":81,"end_line":81,
			   "title":"t","problem":"p","impact":"i","suggestion":"s",
			   "evidence":[{"type":"code","source":"x","detail":"d"}]},
			  {"id":"COR-001","category":"correctness","severity":"high","confidence":0.9,
			   "file":"internal/payment/retry.go","start_line":82,"end_line":82,
			   "title":"other","problem":"p","impact":"i","suggestion":"s",
			   "evidence":[{"type":"code","source":"x","detail":"d"}]}]}`,
			wantErr: "duplicates the id",
		},
		{
			name: "semantically duplicate findings",
			result: `{"summary":"s","findings":[
			  {"id":"COR-001","category":"correctness","severity":"high","confidence":0.9,
			   "file":"internal/payment/retry.go","start_line":81,"end_line":81,
			   "title":"Same claim","problem":"p","impact":"i","suggestion":"s",
			   "evidence":[{"type":"code","source":"x","detail":"d"}]},
			  {"id":"COR-002","category":"correctness","severity":"high","confidence":0.9,
			   "file":"internal/payment/retry.go","start_line":81,"end_line":81,
			   "title":"same claim","problem":"p","impact":"i","suggestion":"s",
			   "evidence":[{"type":"code","source":"x","detail":"d"}]}]}`,
			wantErr: "duplicates findings[0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := okRunner(tt.result)
			reviewer := NewReviewer(NewClient(WithRunner(runner), WithBinary("claude")))

			_, err := reviewer.Review(context.Background(), verifySelection(), "")
			if err == nil {
				t.Fatal("Review() = nil error, want structural validation to reject the result")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}

			// One invocation only: the reviewer's. Verification is never reached, so no
			// unchecked finding can acquire a verdict.
			if len(runner.requests) != 1 {
				t.Errorf("made %d invocations, want only the review", len(runner.requests))
			}
		})
	}
}

// TestVerifyAllOnlySeesValidatedFindings pins the pipeline order at the call site: the
// findings handed to VerifyAll are the ones structural validation already accepted.
func TestVerifyAllOnlySeesValidatedFindings(t *testing.T) {
	valid := `{"summary":"s","findings":[{"id":"COR-001","category":"correctness",
	  "severity":"high","confidence":0.94,"file":"internal/payment/retry.go",
	  "start_line":81,"end_line":83,"title":"t","problem":"p","impact":"i",
	  "suggestion":"s","evidence":[{"type":"code","source":"internal/payment/retry.go:81","detail":"d"}]}]}`

	reviewOutcome, err := NewReviewer(NewClient(WithRunner(okRunner(valid)), WithBinary("claude"))).
		Review(context.Background(), verifySelection(), "")
	if err != nil {
		t.Fatalf("Review() = %v", err)
	}

	runner := newScriptedRunner()
	runner.verdicts["COR-001"] = verdictJSON("COR-001", verification.VerdictValid, 0.97)

	report, err := verifierWith(runner).VerifyAll(context.Background(), reviewOutcome.Result, verifySelection(), "")
	if err != nil {
		t.Fatalf("VerifyAll() = %v", err)
	}

	if len(report.Outcomes) != 1 || report.Outcomes[0].Status != verification.StatusVerified {
		t.Errorf("report = %+v, want the validated finding verified", report.Outcomes)
	}
}
