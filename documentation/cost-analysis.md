# Cost analysis

Synapass is a cost intelligence platform for AI inference: every request gets
an exact, explainable, request-level cost account, and the dashboard turns
those accounts into spend analytics, budgets, forecasts, anomaly flags and a
measured savings ledger.

## How a request is priced

Two numbers are computed for every served request:

- **Estimate** — the routing-time projection (`RouteDecision.EstimatedCost`),
  priced at full input rates because cache state is unknowable in advance.
- **Actual** — the final account, computed after execution from the attempt
  history and the resolved price sheet.

Both are stored on the billing row (`usage_records.cost_usd`,
`estimate_cost_usd`), so estimate quality is always a query, never a guess.

### The engine (`internal/cost`)

`Compute` bills token lines per attempt — fresh input, cached input, output —
plus per-attempt base fees where the sheet charges them. Rules that matter:

- Every line is rounded to whole **micro-dollars**, and the total is defined
  as the sum of the rounded lines. Invoices always foot.
- Retries and fallbacks accumulate: each trace attempt is billed at its own
  target's rates, because the provider metered each call.
- Failed or rejected requests bill **zero**. A request that never reached a
  provider has no lines, even when a sheet carries a base fee.
- Cached tokens bill at the sheet's cached rate, falling back to the full
  input rate when the sheet names no discount. "Free" is a claim about the
  provider's billing that a sheet omitting it cannot support.
- An unpriced runtime (a local model with no sheet) bills exactly zero: that
  is the correct answer, not an unknown.

`Accuracy` scores one estimate against its actual (1.0 is perfect foresight);
the window figure is spend-weighted so a perfect cent cannot outweigh a
drifting hundred dollars.

### The pricing registry

Versioned sheets live in `pricing_versions`, minted via
`POST /admin/v1/cost/pricing`. There is no update endpoint: a price change
is a new row with a later `effective_from`, so last month's invoice
recalculates from last month's sheets.

Resolution precedence is fixed — **tenant > model > provider > global** —
with ties broken by latest `effective_from`. When nothing is effective, the
static registry price applies and the row records `pricing_source:
registry`. Every row records which sheet billed it (`pricing_version_id`,
`pricing_source`) plus the full line-item breakdown, so any historical
request re-audits exactly.

Sheets carry input/output/cached rates per million tokens, an optional
per-attempt base fee, and a currency label. Currency is informational: the
engine computes in the quoted units and never converts, because silent FX
would produce invoices nobody can reproduce from the sheet.

On the request path, sheets resolve through a one-minute TTL cache
(`internal/cost.PriceCache`): the steady state is a map lookup, and every
lookup failure degrades to registry pricing rather than breaking inference.

### Request cost traces

`GET /admin/v1/cost/requests/{requestID}` returns the billing row with its
breakdown, tokens, attempts, estimate, accuracy and pricing provenance —
the complete answer to "what did this cost, and why".

## Analytics

All cost endpoints live under `/admin/v1/cost/` (reads need `usage:read`;
pricing creation and anomaly resolution need full admin):

| Endpoint | Answers |
|---|---|
| `GET /cost/overview` | Totals, accuracy, $/1k tokens, measured savings |
| `GET /cost/by?dimension=` | Spend by provider, model, tenant, endpoint |
| `GET /cost/series` | Actual vs estimated over time |
| `GET /cost/requests/top` | Most expensive requests with breakdowns |
| `GET /cost/requests/{id}` | One request's exact trace |
| `GET+POST /cost/pricing` | The versioned sheets |
| `GET /cost/budgets` | Budget status, burn, alerts |
| `GET /cost/anomalies` | Open spikes (detect-on-read) |
| `GET /cost/forecast` | Month-end projection with band |
| `GET /cost/savings` | Cache, routing and fallback savings |
| `GET /cost/export?format=` | CSV/JSON billing rows for finance |

The console's **Cost** page (`/cost`) renders all of it: KPI cards, the
actual-vs-estimate trend, four spend breakdowns, top-request inspection with
line items, budget burn bars, forecast range, anomaly inbox, the savings
ledger and the pricing registry with a mint-a-sheet form.

## Budgets

`GET /admin/v1/cost/budgets` evaluates every budget against authoritative
usage spend (not the advisory Redis counter): utilization, remaining, burn
rate (1.0 is exactly on pace), projected end-of-period spend, and exhausted
state. Threshold crossings fire `budget_alerts` exactly once per
budget/threshold/period — the unique constraint is the dedup mechanism, and
evaluation is lazy (computed when read) so the numbers are always fresh.

## Forecasting

The month-end projection uses the trailing seven days: mean daily spend plus
a least-squares trend, with past days kept as actuals and only the future
modeled. The band is one residual standard deviation — a range finance can
plan against, not a false point promise.

## Anomaly detection

Each read compares yesterday's spend per provider, model, tenant and
endpoint against the trailing-14-day **median** (a median, so one spike
cannot launder the next). A day counts as anomalous past 3× baseline
(warning) or 10× (critical), with a $10 floor so noise never pages. New
flags persist deduped against open anomalies; operators acknowledge them
from the dashboard or `POST /cost/anomalies/{id}/resolve`.

## Savings ledger

Three measured figures, each from recorded rows:

- **Cache** — estimates on zero-cost cache serves: what serving them would
  have billed.
- **Routing** — the shaping bracket (`cost_before − cost_after`) where
  optimization lowered the eligible cost.
- **Fallback** — per fallback request, the primary's estimate minus the
  chain's actual, floored at zero. An expensive fallback scores nothing,
  never negative: reliability doing its job on a costly model is not a
  debt.

## Price audit and replay

Because rows carry their sheet id, source and line items, any window
re-aggregates under any assumption: export the rows, substitute a sheet,
and compare simulated against actual spend. `pricing_versions` is the full
price-change history, and nothing in it is ever edited.

## Schema

Postgres migration `0011_cost_phase` adds `pricing_versions`,
`budget_alerts`, `cost_anomalies`, extends `usage_records` (estimate,
sheet ref, breakdown, before/after bracket, endpoint), and extends the audit
vocabulary. ClickHouse migration `0003_cost` carries estimate, pricing
source and endpoint into `usage_events`. Money that must be exact uses
`NUMERIC(20,6)`; pre-existing `DOUBLE` columns are untouched.
