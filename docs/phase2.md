# CoreRouter Phase 2 — policy-driven inference control plane

Phase 2 upgrades the Phase 1 gateway into an explainable control plane. Every
request flows through ten visible stages, each recorded in traces and metrics:

```
1. authenticate API key
2. load tenant + endpoint policy
3. classify task (rules-first, deterministic)
4. compute prompt shaping plan
5. check cache (exact → prefix → semantic)
6. evaluate health + provider scores
7. select route (primary + fallback chain)
8. execute provider call
9. fall back if needed
10. persist traces, metrics, scores, audit
```

## Policy engine

`internal/policy/engine.go` evaluates fine-grained rules and produces a
`PolicyDecision` the router consumes directly. Checks run in fixed order so
deny reasons are deterministic: scope → batch/interactive → size → model/provider
allow/deny → region → sensitivity → cost/latency. The first failure denies with
a `deny_reason` visible to admins; warnings (near-budget, clamped latency) do
not block.

Match extensions (`PolicyMatch`): `endpoint_ids`, `task_types`, `batch`,
`regions`, `data_sensitivity`. Limits extensions (`PolicyLimits`):
`allowed/denied_providers`, `allowed/denied_regions`, `denied_sensitive`,
`max_request_bytes`, `batch_only`/`interactive_only`, cache and shaping controls.

## Task classification

`internal/classifier` is rules-first: tools → tool-use, response_format →
structured output, long prompts → long-context, huge non-streaming → batch,
keywords → summarization/translation/extraction/reasoning, code shapes →
coding, short streaming chat → interactive hint. Confidence is coarse
(1.0 structural, 0.9/0.8 heuristic) and signals are named for traces. Python
`task_classify.py` mirrors the taxonomy for async analysis.

## Intelligent routing

`internal/routing/intelligent.go` hooks into the engine without rewriting it:
guardrail filtering (kill switches), region/latency policy notes,
score-informed reordering (bounded ±25% delta, stable sort), task-aware
preferences (batch → cheapest, interactive → fastest, tool/JSON → most capable),
and derived timeout/retry/prompt strategies recorded on the decision.

Routing output includes primary, fallback chain, timeout/retry/shaping
strategies, cache verdict, and score notes. Rejected targets carry reasons.

## Prompt shaping

`internal/shaping` normalizes system messages, compresses whitespace, trims to
history/token budgets (system messages preserved), injects prefixes/suffixes
and guardrails, and sets structured/tool hints. Every step is listed in
`PromptShapePlan.Steps` with token deltas, visible in traces.

## Caching

`internal/cache` implements three tiers over Redis bodies + in-process semantic
index: exact (full normalized hash), prefix (prompt prefix hash), semantic
(cosine over word-bag embeddings, threshold 0.92). Sensitive requests and
`X-CoreRouter-No-Cache` bypass; `cache_entries` holds durable metadata.
`GET /admin/v1/cache/stats` and `POST /admin/v1/cache/invalidate` operate it.
Streaming responses are never cached; only non-streaming JSON is stored.

## Provider scoring

`internal/scoring` blends success (0.5), latency (0.25), cost (0.15) and
feedback (0.1) into an explainable [0,1] score with a human-readable
`explanation`. `GET /admin/v1/scores` exposes providers and models. Workers
`provider_scores.py` mirrors the blend for rollups.

## Eval and replay

`internal/replay` validates and runs replay jobs (bounded to 1000 requests);
`internal/eval` scores outputs deterministically (token_f1, char n-gram, exact
match) and flags regressions against golden thresholds. Workers implement the
same: `replay.py` plans, `jobs.py` dispatches `replay` jobs, `scoring.py`
scores. `POST /admin/v1/replay`, `GET /admin/v1/replay`, `GET
/admin/v1/evaluations` operate jobs; results flow via NATS JetStream.

## Guardrails

`internal/guardrails` holds kill switches, tenant emergency overrides, hard
budget caps (tightest wins), fallback blocks, forced circuit states and rate
escalations. Deny reasons are visible (`policy_deny`, `kill_switch`,
`tenant_override`). `POST /admin/v1/providers/{id}/kill` toggles; `PUT
/admin/v1/budgets` sets caps.

## Observability

New metrics: `classifier_requests_total{task}`, `shaping_requests_total{step}`,
`guardrail_blocks_total{kind}`, `eval_jobs_total`, `replay_jobs_total`,
`feedback_events_total`, `scoring_provider_score`. New trace spans:
`classifier`, `shaping`, `scoring`, `guardrail`, `eval`. Every request carries
task, policy verdict, shaping, cache kind and score notes into ClickHouse
`request_intelligence` and Prometheus.

## Dashboard

New pages: `/scores` (rankings + explanations), `/cache` (hit rates +
invalidate), `/replay` (job creation + progress), `/evaluations` (runs +
regression flags), `/endpoints` (overrides), `/audit` (control-plane history).
Providers gain Kill/Revive; Policies show task/endpoint/region matches;
Requests gain an explain view (`/requests/{id}/explain`).

## Migrations

- `0002_phase2.sql` (Postgres): policy_decisions, task_classifications,
  prompt_shapes, cache_entries, provider_scores, model_scores, replay_jobs,
  evaluation_runs/results, audit_overrides, circuit_breaker_state,
  feedback_events, endpoints.
- `0002_phase2.sql` (ClickHouse): request_intelligence, provider_scores,
  eval_results, cache_events.

## Configuration

```yaml
cache:
  semantic_enabled: true
  prefix_enabled: true
  semantic_threshold: 0.92
classifier: { enabled: true, long_context_tokens: 32000, batch_tokens: 64000 }
shaping: { enabled: true, max_history_messages: 50 }
scoring: { enabled: true, window: 1h }
guardrails: { enabled: true }
eval: { enabled: true, max_requests: 100 }
```

Env: `CR_CACHE_*`, `CR_CLASSIFIER_*`, `CR_SHAPING_*`, `CR_SCORING_*`,
`CR_GUARDRAILS_ENABLED`, `CR_EVAL_*`. See `config.example.yaml`.
