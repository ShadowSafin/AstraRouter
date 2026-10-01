# CoreRouter intelligence workers

Python services for the parts of CoreRouter that benefit from Python: offline
scoring, prompt analysis, evaluation and telemetry rollups. **Nothing here is on
the critical path of a completion** — the gateway is Go, and a worker being down
costs you evaluation results and dashboards, never inference.

## Why this tier exists

Two jobs are genuinely easier in Python than in Go:

- **Evaluation and replay.** Scoring an answer set, processing a dataset, running
  an experiment. This is data work, and the ecosystem for it is Python's.
- **Prompt analysis.** Feature extraction over captured prompts, where the rules
  change far more often than the gateway does.

Everything else — routing, fallback, retries, policy, auth, telemetry fan-out —
stays in Go, where it runs once per request and must not allocate.

## Layout

```
corerouter_workers/
  config.py            environment-driven settings, no secrets from files
  models.py            typed payloads mirroring the Go JSON
  scoring.py           deterministic text metrics (stdlib only)
  prompt_analysis.py   static prompt inspection (stdlib only)
  telemetry.py         rolling per-provider aggregates (stdlib only)
  jobs.py              job dispatch: eval, prompt analysis, replay
  worker.py            the runtime: subscriptions, rollups, health
  bus.py               NATS wrapper; nats-py imported lazily
  streams.py           JetStream stream definitions
  metrics.py           Prometheus counters; no-op fallback
  logging_setup.py     JSON logs with prompt redaction
  judge.py             optional offline judge model client
  cli.py               serve / score / analyze / metrics
tests/                 162 stdlib unittest cases
```

**The pure modules import nothing outside the standard library.** `bus`,
`metrics` and `judge` import `nats-py`, `prometheus-client` and `httpx` inside the
functions that need them. That is what lets the whole test suite run on a bare
interpreter:

```bash
python -m unittest discover -s tests -t .
```

## Running

### Local

```bash
pip install -r requirements.txt
python -m corerouter_workers.cli serve
```

### Docker

```bash
docker build -t corerouter-workers .
docker run --rm -e CR_WORKER_NATS_URL=nats://host.docker.internal:4222 \
  -p 9101:9101 corerouter-workers
```

### Configuration

Every setting is an environment variable with a working default; see
[`.env.example`](.env.example). A JSON or YAML file can be supplied with
`--config` and is applied *before* the environment, so the environment always
wins. YAML needs PyYAML; JSON never does.

## Command line

The one-shot commands need no broker and no third-party dependency, which makes
them usable for debugging on the host that runs the gateway.

```bash
# What metrics can a job request?
corerouter-worker metrics

# Score candidates against a reference, offline.
corerouter-worker score \
  --reference "Paris is the capital of France" \
  --candidate a="Paris is the capital of France" \
  --candidate b="Lyon is the capital of France"

# What would routing decide for this prompt?
corerouter-worker analyze --prompt "Explain how DNS resolution works"
corerouter-worker analyze --request captured-request.json --json

# Serve until SIGTERM.
corerouter-worker serve

# Drain the current backlog and exit (Kubernetes Job shape).
corerouter-worker serve --once
```

## Message contract

Subjects are defined in `bus.py` and must match `internal/storage/nats.go`
exactly. The worker consumes from `COREROUTER_USAGE` and `COREROUTER_JOBS` and
publishes results back.

| Subject | Direction | Payload |
| --- | --- | --- |
| `cr.usage.recorded` | consumed | `UsageRecord` |
| `cr.eval.job` | consumed | job with a `kind` discriminator |
| `cr.eval.result` | published | `EvalResult` |
| `cr.prompt.analysis` | published | `PromptAnalysis` |
| `cr.telemetry.rollup` | published | period summary every 30s |

Job kinds are `eval`, `prompt_analysis` and `replay`. Replay is acknowledged as
**skipped** rather than failed, so a control plane that enqueues it does not build
a redelivery backlog against a worker that will never handle it.

### Example job

```json
{
  "id": "job-1",
  "kind": "eval",
  "tenant_id": "t-1",
  "reference": "Paris is the capital of France.",
  "metrics": ["token_f1", "jaccard"],
  "weights": {"token_f1": 1.0},
  "candidates": [
    {"id": "a", "provider": "openai", "model": "gpt-4o", "output": "Paris is the capital of France."},
    {"id": "b", "provider": "anthropic", "model": "claude", "output": "Lyon is the capital of France."}
  ]
}
```

## Design decisions worth knowing

**Unrecognised fields are ignored, always.** A control plane one release ahead of
a worker pool must not break it. Every `from_dict` reads the keys it knows and
skips the rest.

**Failures are published, not raised.** A job that fails produces a `failed`
result on the results stream. A job that failed silently would be redelivered
until its delivery limit and then discarded, leaving no record of why an
evaluation never ran.

**Malformed payloads are discarded, not retried.** Bytes that will never parse
would just burn the delivery budget.

**Errors are never scored.** A candidate carrying an `error` is reported as
skipped. Scoring an error message against a reference would enter a zero into an
average and quietly drag a provider's reported quality down.

**Rankings are reproducible.** Ties are broken by candidate id, so the same job
always produces the same ranking. A ranking that reshuffles between runs makes an
A/B comparison between two providers uninterpretable.

**Cancellations do not count as errors.** A client that closes the connection has
not discovered a provider fault; counting it would make impatient users look like
an outage.

**No verdict below ten requests.** A single failure is not evidence of a degraded
provider, so health reports `unknown` and the router keeps it eligible.

**The judge is optional and local.** Judging a candidate set with a hosted model
would make the eval worker depend on the providers it exists to measure. A judge
verdict is stored separately from the deterministic scores, with its identity
attached.

## Observability

Metrics are served on `CR_WORKER_METRICS_ADDR` (default `0.0.0.0:9101`) with the
same `corerouter_` prefix and `_total`/`_seconds` suffixes as the Go control
plane, so one scrape config and one dashboard cover both processes.

The port doubles as the liveness endpoint. A worker whose consumer loop has died
stops incrementing `corerouter_worker_jobs_total`, which is the only externally
visible symptom a background process has — alert on that rather than on the port.

Logs are JSON by default with `time`, `level`, `msg` and `service` keys matching
the gateway's. Prompts, completions and anything key-shaped are redacted before
they reach a handler.

## Testing

```bash
python -m unittest discover -s tests -t .   # 162 cases, no install required
pytest tests/                               # same suite via pytest
ruff check corerouter_workers tests
mypy corerouter_workers
```
