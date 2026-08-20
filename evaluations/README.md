# Evaluation datasets

This directory holds the ground truth ARC is measured against, and the captured runs
measured against it. Nothing here calls a model: `arc evaluate` is deterministic, and
no LLM judges another LLM's output.

The bundled `seed-labels.json` and `seed-predictions.json` are an **illustrative
format fixture**, not a measurement of ARC. Real numbers require real labelled pull
requests, which is what the workflow below produces.

## Why this comes first

Semantic retrieval, specialist reviewers, and prompt changes all claim to improve
review quality. Without a fixed label set and a capture path, each of those is an
architectural opinion. With them, every later change is a number that moved — or
didn't.

## Building a labelled suite

**1. Choose 15–25 merged pull requests.** Prefer ones whose review history tells you
what was actually wrong: a review comment that led to a fix, a follow-up bug, a
revert. Include some pull requests that were genuinely clean — a suite with an issue
in every case cannot measure false positives, which is the failure mode that costs a
reviewer's trust.

Start from [`real-labels.template.json`](real-labels.template.json) — it is a shape to
copy, not data. Every value in it is a placeholder, and it is deliberately not named
`real-labels.json` so an unedited template can never be scored as if it were ground
truth.

**2. Label each one by hand,** from the human record rather than from an ARC run.
Labelling what ARC found makes the score meaningless. One case per pull request:

```json
{
  "id": "acme/payments#123",
  "description": "Permanent declines were being retried.",
  "source": "https://github.com/acme/payments/pull/123",
  "findings": [
    {
      "id": "REQ-001",
      "category": "requirement",
      "file": "internal/payment/retry.go",
      "start_line": 84,
      "end_line": 94,
      "title_contains": ["retry", "decline"]
    }
  ]
}
```

- `id` must match the case ID used at capture time. The default is `owner/repo#number`.
- Line ranges are matched by **overlap**, so label the range a reviewer would point
  at, not an exact span.
- `title_contains` is optional and case-insensitive. Use it only to disambiguate two
  issues of the same category whose ranges overlap.
- A clean case is `"findings": []`. It affects precision, never recall.

**3. Keep the label file fixed.** Editing labels to match a run is how a benchmark
stops measuring anything. New knowledge means a new labelled case or a new version of
the file, not a quiet edit.

## Capturing a run

`--capture-predictions` writes one snapshot per reviewed pull request. It records the
findings the reviewer proposed and the domain model validated — deliberately *before*
verification and publication policy, because precision and recall are properties of
the proposal, while suppression is already reported with its own reasons. Capturing
post-policy findings would make every threshold change look like a model change.

```sh
mkdir -p evaluations/runs/baseline

arc review \
  --pr https://github.com/acme/payments/pull/123 \
  --repo-dir ../payments \
  --claude \
  --capture-run baseline \
  --capture-predictions evaluations/runs/baseline/payments-123.json
```

Repeat for every labelled pull request, keeping `--capture-run` identical across the
suite. Capture never overwrites: a re-run of a case reports the collision rather than
replacing evidence, so delete the snapshot deliberately if you mean to replace it.

## Scoring

`--predictions` accepts a single snapshot or a directory of them. A directory is
merged in case-ID order, and mixing run names, models, or prompt versions in one
directory is refused — an averaged score across configurations is one no single
configuration achieved.

```sh
arc evaluate \
  --labels evaluations/real-labels.json \
  --predictions evaluations/runs/baseline

arc evaluate \
  --labels evaluations/real-labels.json \
  --predictions evaluations/runs/baseline \
  --format json > evaluations/runs/baseline.report.json
```

The report gives aggregate and per-category precision, recall, and F1; per-case false
positives and missed labels; and how many clean cases stayed clean.

## Iterating on the same pull request

A re-review is not a fresh case. When the author pushes fixes and ARC reviews again,
the published review records what it reported, and the next run reads that back: a
finding reported before is placed in the body rather than commented on the diff again,
and the body opens with what is no longer reported.

For measurement, capture each round as its own case ID — `owner/repo#123@round1`,
`@round2` — and label each round separately. Rounds are different states of the code,
and scoring them as one case would hide exactly what you want to see: whether the
second review was quieter than the first about the same lines.

## Comparing experiments

Capture each experiment under its own run name and directory, then compare reports:

```
evaluations/
  real-labels.json            fixed ground truth
  runs/
    baseline/                 diff-only context
    symbol-retrieval/         + retrieval of unchanged code
    specialists/              + security and pattern reviewers
```

Two things to read besides F1: whether recall gains came with a precision loss, and
whether the clean cases stayed clean. A change that finds one more real issue and adds
three false positives is a regression in the only currency that matters here, which is
a reviewer's willingness to keep reading.
