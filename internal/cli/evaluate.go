package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/your-company/agentic-code-review/internal/evaluation"
)

func runEvaluate(args []string) error {
	fs := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	labelsPath := fs.String("labels", "", "human-labelled ground truth JSON")
	predictionsPath := fs.String("predictions", "", "captured reviewer predictions JSON file, or a directory of snapshots")
	format := fs.String("format", "markdown", "output format: markdown or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *labelsPath == "" {
		return errors.New("--labels is required")
	}
	if *predictionsPath == "" {
		return errors.New("--predictions is required")
	}
	if *format != "markdown" && *format != "json" {
		return fmt.Errorf("invalid format %q: expected markdown or json", *format)
	}

	labelsFile, err := os.Open(*labelsPath)
	if err != nil {
		return fmt.Errorf("open labels: %w", err)
	}
	defer labelsFile.Close()

	labels, err := evaluation.DecodeLabels(labelsFile)
	if err != nil {
		return err
	}

	// A file is one captured run; a directory is a suite of per-pull-request
	// captures from the same run, merged deterministically.
	predictions, err := evaluation.LoadPredictions(*predictionsPath)
	if err != nil {
		return err
	}

	report := evaluation.Evaluate(labels, predictions)
	if *format == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("encode evaluation report: %w", err)
		}
		return nil
	}

	fmt.Print(evaluation.Render(report))
	return nil
}
