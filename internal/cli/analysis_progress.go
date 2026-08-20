package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/your-company/agentic-code-review/internal/analysis"
)

// analysisProgressRunner makes long-running deterministic checks visible while
// retaining the normal bounded capture used by the final report and Claude
// context. It reports lifecycle events rather than streaming every build line,
// which would let a large sbt suite overwhelm the review output.
type analysisProgressRunner struct {
	inner             analysis.Runner
	out               io.Writer
	heartbeatInterval time.Duration
}

func newAnalysisProgressRunner(inner analysis.Runner) *analysisProgressRunner {
	return &analysisProgressRunner{inner: inner, out: os.Stdout, heartbeatInterval: 30 * time.Second}
}

func (r *analysisProgressRunner) Run(ctx context.Context, dir string, check analysis.Check) analysis.CheckResult {
	fmt.Fprintf(
		r.out,
		"  RUN      %-18s %s (timeout %s)\n",
		check.Name,
		check.DisplayCommand(),
		check.EffectiveTimeout(),
	)

	started := time.Now()
	stopHeartbeat := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	go func() {
		defer close(heartbeatStopped)
		if r.heartbeatInterval <= 0 {
			return
		}

		ticker := time.NewTicker(r.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Fprintf(
					r.out,
					"  RUNNING  %-18s elapsed %s\n",
					check.Name,
					time.Since(started).Round(time.Second),
				)
			case <-stopHeartbeat:
				return
			}
		}
	}()

	result := r.inner.Run(ctx, dir, check)
	close(stopHeartbeat)
	<-heartbeatStopped
	fmt.Fprintf(
		r.out,
		"  %-8s %-18s completed in %s\n",
		checkStatus(result),
		check.Name,
		result.Duration.Round(100*time.Millisecond),
	)
	return result
}

// LookupTool preserves the optional capability of the wrapped command runner,
// allowing Analyzer to report a missing build tool as SKIP before Run is called.
func (r *analysisProgressRunner) LookupTool(name string) error {
	if locator, ok := r.inner.(analysis.ToolLocator); ok {
		return locator.LookupTool(name)
	}
	return nil
}

func printAnalysisProgressHeader() {
	fmt.Println()
	fmt.Println("Running deterministic checks")
	fmt.Println()
}
