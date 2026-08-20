package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/your-company/agentic-code-review/internal/analysis"
)

type progressRunnerFixture struct {
	result      analysis.CheckResult
	lookupError error
	delay       time.Duration
}

func (f progressRunnerFixture) Run(context.Context, string, analysis.Check) analysis.CheckResult {
	time.Sleep(f.delay)
	return f.result
}

func (f progressRunnerFixture) LookupTool(string) error { return f.lookupError }

func TestAnalysisProgressRunnerReportsLongRunningCheckLifecycle(t *testing.T) {
	var out bytes.Buffer
	runner := &analysisProgressRunner{
		inner: progressRunnerFixture{result: analysis.CheckResult{
			Name: "sbt-compile", Command: "sbt -batch compile", Passed: true, ExitCode: 0, Duration: 91 * time.Second,
		}, delay: 25 * time.Millisecond},
		out:               &out,
		heartbeatInterval: 5 * time.Millisecond,
	}
	check := analysis.Check{
		Name: "sbt-compile", Command: "sbt", Args: []string{"-batch", "compile"}, Timeout: 5 * time.Minute,
	}

	result := runner.Run(context.Background(), "/tmp/repo", check)

	if !result.Passed {
		t.Fatal("Run() dropped the wrapped result")
	}
	for _, want := range []string{
		"RUN", "sbt-compile", "sbt -batch compile", "timeout 5m0s", "RUNNING", "elapsed", "PASS", "completed in 1m31s",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("progress output %q does not contain %q", out.String(), want)
		}
	}
}

func TestAnalysisProgressRunnerPreservesToolLookup(t *testing.T) {
	want := errors.New("sbt missing")
	runner := &analysisProgressRunner{inner: progressRunnerFixture{lookupError: want}, out: &bytes.Buffer{}}

	if got := runner.LookupTool("sbt"); !errors.Is(got, want) {
		t.Errorf("LookupTool() = %v, want %v", got, want)
	}
}
