# Contributing to AstraRouter

Thanks for taking the time. This document covers what a good change looks like
here, so review is fast and nobody has to reverse-engineer intent from a diff.

**In short:** match the surrounding code, add tests for behaviour you change,
explain *why* in the commit message, and update the documentation when you change
behaviour.

Contributions are accepted under the [Apache License 2.0](LICENSE), the same terms
as the project.

## Getting set up

```bash
git clone https://github.com/shadowsafin/astrarouter.git
cd astrarouter

cp .env.example .env
docker compose up -d --build
```

You do not need provider keys to develop. The gateway, the admin surface and the
dashboard all work without a live upstream; only a real completion needs one.

Verify your baseline before changing anything:

```bash
make test                        # Go + Python
make dashboard-typecheck
bash scripts/smoke.sh            # against the running stack
```

If a test fails before your change, say so in the pull request — it is useful
information, not a blocker.

## Repository layout

| Path | Contains |
| --- | --- |
| `cmd/astrarouter/` | The CLI entrypoint |
| `internal/domain/` | Types and rules. No I/O, no dependencies on other packages. |
| `internal/config/` | Defaults, file and environment loading, validation, redaction |
| `internal/providers/` | One adapter per provider kind, plus the shared HTTP client |
| `internal/routing/` | The engine, executor, health tracker, catalogue |
| `internal/policy/` | Resolution, fine-grained rules, rate limits, budgets |
| `internal/cache/`, `internal/classifier/`, `internal/shaping/`, `internal/scoring/`, `internal/guardrails/` | The intelligence stages |
| `internal/tools/` | Registry, single invocation, bounded loop |
| `internal/storage/` | Postgres, Redis, ClickHouse, NATS, embedded migrations |
| `internal/telemetry/` | Metrics, traces, the async record pipeline |
| `internal/api/` | The HTTP surface: inference, health, admin |
| `internal/bootstrap/` | Wiring |
| `workers/` | The Python intelligence tier |
| `dashboard/` | The Next.js console |
| `deploy/` | Dockerfiles, systemd units, observability configuration |
| `documentation/` | Everything about how the system works |

Start reading at `internal/api/chat.go` for the request path end to end.

## Code style

### Go

- `gofmt`, always. `make fmt` formats and then fails on anything unformatted.
- Comments explain **why**, not what. The code already says what.
- Keep the domain package free of I/O. If a change needs it, the type does not
  belong in `domain`.
- New provider behaviour goes in that provider's adapter. Shared HTTP behaviour
  goes in `httpclient.go`; shared response handling in `base.go`.
- Errors are normalized through `domain.NewError`, so a caller can branch on a
  code rather than a string.

### Python workers

- `ruff` for formatting and lint; `mypy` for types. `make lint`.
- Workers are for asynchronous, CPU-bound work. Nothing here may run inline with
  an inference request — that separation is structural, not a convention.

### Dashboard

- `npm run typecheck` must pass.
- A page is a thin route entry in `src/app/<name>/page.tsx`; the view behind it
  lives in `src/components/views/`.
- Add a sidebar entry when you add a page.
- Primitives go in `src/components/ui/`. Do not duplicate a card or a table.

## Testing

| Change | Expect |
| --- | --- |
| Bug fix | A test that fails before the fix and passes after it |
| New behaviour | Tests for the behaviour and its boundaries |
| Refactor | The existing tests unchanged and green — that is the point |
| Dashboard page | `npm run typecheck` plus a `200` from the running stack |
| Migration | Applied by `astrarouter migrate` against a real database |

```bash
go test ./internal/...                                  # Go
go test ./internal/routing/ -count=1 -run TestName -v   # one test
cd workers && python -m unittest discover -s tests -t . # Python
make test                                               # Go + Python
```

Run tests with `-count=1` when you are chasing a flake; the cache will otherwise
hide a failure.

## Commit messages

The history is the project's primary record of why it looks the way it does. Each
commit should be one coherent change.

Use Conventional Commit prefixes:

```
feat(routing): add a sticky-affinity strategy
fix(providers): stop sending top_k to OpenAI proper
docs(database): document the retention model
refactor(telemetry): extract the label builder
test(cache): cover the prefix length gate
chore(deps): bump clickhouse-go to 2.48.0
```

Scopes are the package or area: `routing`, `providers`, `api`, `cache`, `tools`,
`dashboard`, `workers`, `storage`, `docs`, `deploy`.

Write the subject as a command, under 72 characters. Explain the *why* in the body
where it is not obvious from the diff:

```
fix(routing): treat the latency target as ranking-only

The soft latency target was also used as the hard per-attempt deadline, so
the default 10s target cancelled every generation longer than ten seconds,
mid-sentence, with nothing reporting the cut.

Generation budgets now come only from the timeout policy. An explicitly
configured budget is never raised, because an operator who set 30s meant it.
```

## Documentation

Behaviour changes need documentation in the same pull request. Where the change
belongs:

| Change | Document |
| --- | --- |
| A new or changed endpoint, header, error code | `documentation/api.md` |
| Routing or policy semantics | `documentation/routing.md` |
| Provider kinds or credential handling | `documentation/providers.md` |
| Cache tiers, policy or invalidation | `documentation/caching.md` |
| Tool execution, bounds or safety | `documentation/tools.md` |
| A new setting | `documentation/installation/docker.md` or `native.md`, plus `config.example.yaml` |
| A new dashboard page | `documentation/dashboard.md` |
| A new table or migration | `documentation/database.md` |
| A new metric or alert | `documentation/observability.md` |
| A new failure mode | `documentation/troubleshooting.md` |
| A design decision | `documentation/architecture.md`, with the failure mode it addresses |

Two rules keep the documentation usable:

1. **Document a fact once.** Link to the owning page rather than restating it.
   The documentation index at `documentation/README.md` is the entry point.
2. **Explain why, not only what.** A future contributor needs the reasoning to
   know when a constraint no longer holds.

## Pull requests

- One logical change per pull request. Split unrelated fixes.
- Write a description that explains the problem and the approach. "Why" is the
  part reviewers cannot infer.
- Call out anything behaviour-changing that an operator would need to know about:
  a new required setting, a migration, a changed default.
- Note anything you verified and how — a live probe, a smoke run, a test.
- Small pull requests get reviewed. Large ones get read once.

## Security

Do not open a public issue for a vulnerability. Follow
[SECURITY.md](SECURITY.md) instead.

Two rules that cover most of it:

- Never commit a secret. `.env`, `*.pem` and `.smoke_key` are gitignored; keep it
  that way.
- Never log a request body or a header value that could carry one. There is a
  redaction list for this reason — extend it rather than working around it.

## Reporting bugs

Open an issue with:

- What you did, what you expected, what happened.
- The version, from `astrarouter version` or `GET /version`.
- The `request_id` from the failing response, if you have one. That single id
  usually resolves the question without any back-and-forth.
- Relevant logs around the failure.

## Code of conduct

Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

---

Related: [README](README.md) · [Architecture](documentation/architecture.md) · [Troubleshooting](documentation/troubleshooting.md) · [Back to the documentation index](documentation/README.md)