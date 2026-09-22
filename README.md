# Circuit Analyzer

`circuit_analyzer.go` analyzes wide CSV exports of network-interface HC Octets metrics. It creates a circuit trend report and a failure/failover report that identifies circuits that may have absorbed traffic following a major drop.

The program uses only the Go standard library. No third-party packages are required.

## Features

### Trend analysis

- Reads timestamped interface measurements from a wide CSV file.
- Ignores companion columns ending in `(units)`.
- Extracts and normalizes circuit identifiers from metric headers.
- Groups matching IN, OUT, and endpoint measurements at the circuit level.
- Compares an early window with a late window in the input file.
- Calculates baseline average, current average, percentage change, and sample counts.
- Sorts results from the largest increase to the largest decrease.

The default comparison uses the first seven days and last seven days in the file.

### Failure and takeover analysis

- Builds a rolling median baseline for each circuit.
- Flags a failure when traffic falls at least 90% below baseline.
- Suppresses repeat alerts for the same circuit within 12 hours.
- Searches the failure sample and following samples for increases on other circuits.
- Ranks takeover candidates using absolute gain, percentage increase, inverse correlation, and displaced-versus-received traffic.

A takeover candidate is evidence of correlated traffic movement, not proof of the configured routing policy. Validate important findings against routing, telemetry, maintenance, and incident records.

## Requirements

- Go 1.20 or later is recommended.
- A CSV file whose first column contains timestamps.
- Metric columns containing circuit or interface measurements.

Recognized timestamp examples:

```text
Thursday, January 29 2026 9:00 pm UTC-05:00 EST
Friday, July 10 2026 1:00 pm UTC-04:00 EDT
2026-01-29T21:00:00Z
2026-01-29 21:00:00
```

## Expected input

```csv
Timestamp,"router-a - HundredGigE0/0/0/3 - T:NWI_M:1_CID:12.VLXM.907740..CBCL.. | PEER: router-b - OUT - HC Octets","router-a - HundredGigE0/0/0/3 - T:NWI_M:1_CID:12.VLXM.907740..CBCL.. | PEER: router-b - OUT - HC Octets (units)"
"Thursday, January 29 2026 9:00 pm UTC-05:00 EST",38.55,
"Thursday, January 29 2026 11:00 pm UTC-05:00 EST",39.10,
```

The program recognizes circuit header patterns including:

```text
T:NWI_M:1_CID:12.VLXM.907740..CBCL.. | PEER:
T:NWI_M1_CID:99.KGFS.001747.1.CBCL.. | PEER:
```

If a header does not match a recognized pattern, the complete header is used as the circuit name.

## Build

Linux or macOS:

```bash
go build -o circuit-analyzer circuit_analyzer.go
```

Windows PowerShell:

```powershell
go build -o circuit-analyzer.exe circuit_analyzer.go
```

## Quick start

Run without compiling:

```bash
go run circuit_analyzer.go \
  -input "Performance Metrics September 2026.csv" \
  -out "2026-09-circuit-analysis"
```

Run the compiled binary on Linux or macOS:

```bash
./circuit-analyzer \
  -input "Performance Metrics September 2026.csv" \
  -out "2026-09-circuit-analysis"
```

Run the compiled binary in Windows PowerShell:

```powershell
.\circuit-analyzer.exe `
  -input "Performance Metrics September 2026.csv" `
  -out "2026-09-circuit-analysis"
```

## Command-line options

| Option | Default | Description |
|---|---:|---|
| `-input` | Required | Input CSV path |
| `-out` | `circuit-analysis` | Output filename prefix |
| `-trend-days` | `7` | Days in the early and late trend windows |
| `-failure-drop` | `90` | Percentage drop required to flag a failure |
| `-baseline-samples` | `12` | Preceding samples used for the rolling baseline |
| `-min-baseline-samples` | `6` | Minimum valid values required for a baseline |
| `-takeover-window` | `2` | Current/following samples examined for takeover traffic |
| `-candidate-rise` | `40` | Minimum candidate traffic increase, in percent |
| `-min-candidate-gain` | `0.5` | Minimum absolute candidate traffic gain |
| `-top` | `5` | Maximum candidates retained per failure |

Display built-in help:

```bash
./circuit-analyzer -h
```

## Recommended monthly command

For measurements sampled every two hours, 12 samples represent approximately 24 hours:

```bash
./circuit-analyzer \
  -input "Performance Metrics October 2026.csv" \
  -out "2026-10-circuit-analysis" \
  -trend-days 7 \
  -failure-drop 90 \
  -baseline-samples 12 \
  -min-baseline-samples 6 \
  -takeover-window 2 \
  -candidate-rise 40 \
  -min-candidate-gain 0.5 \
  -top 5
```

For hourly measurements, consider using:

```bash
-baseline-samples 24
```

## Output files

For `-out 2026-10-circuit-analysis`, the program creates:

```text
2026-10-circuit-analysis-trend.csv
2026-10-circuit-analysis-failures.csv
```

It also prints a concise summary to the terminal.

### Trend report fields

| Field | Description |
|---|---|
| `circuit` | Normalized circuit identifier or original metric header |
| `baseline_average` | Average traffic during the early window |
| `current_average` | Average traffic during the late window |
| `percent_change` | Change from baseline to current |
| `baseline_samples` | Valid observations in the baseline window |
| `current_samples` | Valid observations in the current window |

Percentage change is calculated as:

```text
((current_average / baseline_average) - 1) * 100
```

### Failure report fields

| Field | Description |
|---|---|
| `failure_time` | Failure detection timestamp |
| `failed_circuit` | Circuit crossing the drop threshold |
| `rolling_baseline` | Median of preceding baseline samples |
| `failure_value` | Traffic value at detection |
| `drop_percent` | Percentage decrease from baseline |
| `estimated_lost_traffic` | Difference between pre-failure and failure-window traffic |
| `candidate_rank` | Rank of a possible takeover circuit |
| `takeover_candidate` | Circuit increasing during the failure interval |
| `candidate_pre` | Candidate pre-failure median |
| `candidate_during` | Candidate maximum during the takeover window |
| `candidate_gain` | Absolute traffic increase |
| `candidate_rise_percent` | Percentage traffic increase |
| `inverse_correlation` | Pearson correlation with the failed circuit |
| `score` | Internal candidate-ranking score |

A single failure may produce several rows because the report retains multiple candidates.

## Interpreting takeover candidates

Evidence is stronger when:

1. Candidate traffic rises at the same time, or immediately after, the failed circuit drops.
2. Candidate gain is reasonably close to the failed circuit's displaced traffic.
3. Candidate traffic has a negative correlation with the failed circuit.
4. The same pairing appears during multiple failure events.
5. A reverse event later moves traffic back to the original path.

Review results carefully when the input has missing samples, very low baselines, coarse sampling, unrelated simultaneous changes, maintenance activity, or values that are not expressed in comparable units.

## Tuning examples

Detect an 80% drop:

```bash
./circuit-analyzer -input "metrics.csv" -failure-drop 80
```

Require stronger takeover evidence:

```bash
./circuit-analyzer \
  -input "metrics.csv" \
  -candidate-rise 75 \
  -min-candidate-gain 2
```

Compare the first and last 10 days:

```bash
./circuit-analyzer -input "metrics.csv" -trend-days 10
```

Retain only the three strongest candidates:

```bash
./circuit-analyzer -input "metrics.csv" -top 3
```

## Suggested monthly workflow

1. Export the month's performance metrics to CSV.
2. Use a consistent filename such as `Performance Metrics 2026-10.csv`.
3. Run the analyzer with a matching output prefix.
4. Review the largest positive and negative circuit trends.
5. Review failure events and top takeover candidates.
6. Compare candidate gain with estimated lost traffic.
7. Validate significant events against routing, alarms, maintenance, and incident timelines.
8. Archive the input and both reports together.

Suggested layout:

```text
network-analysis/
├── circuit_analyzer.go
├── README.md
└── reports/
    ├── 2026-09/
    │   ├── Performance Metrics 2026-09.csv
    │   ├── 2026-09-circuit-analysis-trend.csv
    │   └── 2026-09-circuit-analysis-failures.csv
    └── 2026-10/
        ├── Performance Metrics 2026-10.csv
        ├── 2026-10-circuit-analysis-trend.csv
        └── 2026-10-circuit-analysis-failures.csv
```

## Troubleshooting

### `Error: -input is required`

Provide the CSV path:

```bash
./circuit-analyzer -input "metrics.csv"
```

### `Error: not enough timestamped rows`

Check that the first column contains supported timestamps and that the file has at least two data rows.

### Full interface headers appear instead of circuit IDs

The header did not match a recognized pattern. Update the expressions in `circuitKey()` for the monitoring platform's header format.

### No failures are detected

No circuit may have crossed the threshold, the rolling baseline may lack valid samples, or the threshold may be too strict. For exploratory analysis:

```bash
-failure-drop 80
```

### Too many takeover candidates

Raise the candidate filters:

```bash
-candidate-rise 75 -min-candidate-gain 2
```

## Methodology notes

- Measurements sharing a circuit identifier are averaged together.
- The program does not convert HC Octets into Mbps or Gbps.
- It assumes compared measurements are normalized and comparable.
- Missing and nonnumeric cells are ignored.
- Rolling baselines use the median to reduce sensitivity to temporary spikes.
- Correlation is calculated across available paired observations in the complete input file.
- Results are operational evidence that should be validated before capacity, routing, or incident decisions.

## License

Add the license appropriate for your organization before distributing the program outside your team.
