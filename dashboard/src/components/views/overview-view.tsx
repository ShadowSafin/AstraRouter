'use client';

import { Activity, Clock, Coins, DatabaseZap, Gauge, Wrench } from 'lucide-react';
import * as React from 'react';

import { BarList } from '@/components/charts/bar-list';
import { LineChart } from '@/components/charts/time-series';
import { ChartDebug } from '@/components/chart-debug';
import { PageHeader } from '@/components/page-header';
import { RangePicker, useWindowParam, windowToFromParam } from '@/components/range-picker';
import { StatCard } from '@/components/stat-card';
import { TenantPicker, useTenantParam } from '@/components/tenant-picker';
import { HealthBadge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ChartSkeleton, CardsSkeleton, EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { useCacheStats, useOverview, useToolInvocations } from '@/hooks/use-admin';
import { bucketLabels, deltaRatio, successRate } from '@/lib/metrics';
import {
  formatCompact,
  formatCurrency,
  formatDurationMs,
  formatNumber,
  formatPercent,
} from '@/lib/format';
import { buildQuery } from '@/lib/api';
import { bucketSeries, bucketTotals, latencySplit, outcomeSplit } from '@/lib/charts';

export function OverviewView() {
  const window = useWindowParam();
  const tenantId = useTenantParam();
  const from = windowToFromParam(window);

  const { data, isPending, isError, error, refetch, isFetching } = useOverview(tenantId, from);
  // Tool-call volume and cache efficiency are product-level signals, not
  // request attributes, so they arrive on their own queries. Both are tiny:
  // one row plus a total, and the cached stats endpoint.
  const { data: toolHistory } = useToolInvocations(1);
  const { data: cache } = useCacheStats();

  const summary = data?.summary;
  const previous = data?.previous_summary;
  const buckets = data?.series ?? [];
  const labels = React.useMemo(() => bucketLabels(buckets), [buckets]);

  const deltaLabel = `previous ${window}`;
  // Totals come from the bucket's own `requests` field, never recomputed from
  // outcome parts: the parts do not partition the whole.
  const requestsSeries = React.useMemo(() => bucketSeries(buckets, 'requests'), [buckets]);
  const requestTotals = requestsSeries.values;
  const outcomes = React.useMemo(() => outcomeSplit(buckets), [buckets]);
  const latencies = React.useMemo(() => latencySplit(buckets), [buckets]);
  const seriesTotal = requestTotals.reduce((total, value) => total + value, 0);

  const toolCalls = toolHistory?.total ?? 0;
  const cacheHitRate = cache?.stats.hit_rate;

  return (
    <>
      <PageHeader
        title="Overview"
        description="Traffic, spend and health for the selected window."
        actions={
          <>
            <TenantPicker value={tenantId} />
            <RangePicker value={window} />
          </>
        }
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      <ChartDebug
        window={window}
        tenantId={tenantId}
        buckets={buckets}
        summaryRequests={summary?.requests}
        seriesTotal={seriesTotal}
        warnings={requestsSeries.warnings}
      />

      {/* The hero card: one question — how much traffic — answered at a glance.
          An empty window shows an empty state, never invented numbers: a
          control plane that charts traffic that did not happen is lying. */}
      <Card className="mb-4 overflow-hidden">
        <CardHeader className="pb-2">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <div>
              <CardTitle className="text-base">API requests</CardTitle>
              <CardDescription>
                {window === '24h' ? 'Last 24 hours' : window === '7d' ? 'Last 7 days' : `Last ${window}`}
                {summary ? ` · ${formatNumber(summary.requests)} total` : ''} · times in UTC
              </CardDescription>
            </div>
            {summary && previous ? (
              <p className="text-xs text-muted-foreground">
                {deltaRatio(summary.requests, previous.requests) !== null &&
                Math.abs(deltaRatio(summary.requests, previous.requests) as number) >= 0.005 ? (
                  <span
                    className={
                      (deltaRatio(summary.requests, previous.requests) as number) > 0
                        ? 'text-success'
                        : 'text-danger'
                    }
                  >
                    {(deltaRatio(summary.requests, previous.requests) as number) > 0 ? '+' : ''}
                    {((deltaRatio(summary.requests, previous.requests) as number) * 100).toFixed(1)}%
                  </span>
                ) : (
                  <span>flat</span>
                )}{' '}
                vs {deltaLabel}
              </p>
            ) : null}
          </div>
        </CardHeader>
        <CardContent>
          {isPending ? (
            <ChartSkeleton />
          ) : buckets.length === 0 ? (
            <EmptyState
              title="No requests in this window"
              description="Traffic will appear here once the gateway serves a request."
            />
          ) : (
            <LineChart
              labels={labels}
              height={260}
              formatValue={(value) => formatCompact(Math.round(value))}
              area
              areaGradient
              series={[{ name: 'Requests', color: 'hsl(222 100% 68%)', values: requestTotals }]}
            />
          )}
        </CardContent>
      </Card>

      {isPending && !summary ? <CardsSkeleton count={6} /> : null}

      {summary ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-6">
          <StatCard
            label="Requests"
            icon={Activity}
            value={formatNumber(summary.requests)}
            delta={{ ratio: deltaRatio(summary.requests, previous?.requests), label: deltaLabel }}
            hint={`${formatNumber(summary.successes)} succeeded`}
          />
          <StatCard
            label="Success rate"
            icon={Gauge}
            value={formatPercent(successRate(summary))}
            delta={{
              ratio: deltaRatio(successRate(summary), previous ? successRate(previous) : undefined),
              label: deltaLabel,
            }}
            tone={successRate(summary) >= 0.95 ? 'success' : 'warning'}
            hint={`${formatNumber(summary.rejections)} rejected`}
          />
          <StatCard
            label="p95 latency"
            icon={Clock}
            value={formatDurationMs(summary.latency_p95_ms)}
            delta={{ ratio: deltaRatio(summary.latency_p95_ms, previous?.latency_p95_ms), label: deltaLabel, higherIsWorse: true }}
            hint={`p50 ${formatDurationMs(summary.latency_p50_ms)}`}
          />
          <StatCard
            label="Estimated cost"
            icon={Coins}
            value={formatCurrency(summary.total_cost_usd)}
            delta={{ ratio: deltaRatio(summary.total_cost_usd, previous?.total_cost_usd), label: deltaLabel, higherIsWorse: true }}
            hint={`avg ${formatCurrency(summary.avg_cost_usd)} / request`}
          />
          <StatCard
            label="Cache hit rate"
            icon={DatabaseZap}
            value={cacheHitRate != null ? formatPercent(cacheHitRate) : '—'}
            tone={cacheHitRate != null && cacheHitRate >= 0.2 ? 'success' : 'default'}
            hint={cache && !cache.enabled ? 'response cache is disabled' : 'exact, prefix and semantic'}
          />
          <StatCard
            label="Tool calls"
            icon={Wrench}
            value={formatNumber(toolCalls)}
            hint="gateway + client executed, all time"
          />
        </div>
      ) : null}

      <div className="mt-4 grid gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Requests by outcome</CardTitle>
            <CardDescription>
              Failures are counted as the client experienced them. A rising fallback line with a flat
              error line means the chain is absorbing a provider problem.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <ChartSkeleton />
            ) : buckets.length === 0 ? (
              <EmptyState title="No requests in this window" description="Traffic will appear here once the gateway serves a request." />
            ) : (
              <LineChart
                labels={labels}
                formatValue={(value) => formatCompact(Math.round(value))}
                series={[
                  { name: 'Success', color: 'hsl(var(--success))', values: outcomes.successes },
                  { name: 'Fallback', color: 'hsl(var(--warning))', values: outcomes.fallbacks },
                  {
                    name: 'Errors',
                    color: 'hsl(var(--danger))',
                    values: outcomes.errors,
                  },
                ]}
              />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Provider health</CardTitle>
            <CardDescription>Live assessment from the routing engine, not a synthetic check.</CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <TableSkeleton rows={4} columns={2} />
            ) : (data?.provider_health ?? []).length === 0 ? (
              <EmptyState title="No providers configured" description="Add a provider to begin routing traffic." />
            ) : (
              <ul className="space-y-3">
                {(data?.provider_health ?? []).map((health) => (
                  <li key={health.provider_id} className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium">{health.provider_name || health.provider_id}</p>
                      <p className="text-xs text-muted-foreground">
                        {formatDurationMs(health.latency_ms)} · {formatPercent(health.error_rate)} errors
                        {health.source ? ` · ${health.source}` : ''}
                      </p>
                    </div>
                    <HealthBadge state={health.state} />
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="mt-4 grid gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Latency percentiles</CardTitle>
            <CardDescription>
              p99 uses the same histogram buckets as the alerting rules, so a spike here is the one the
              pager will see.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <ChartSkeleton />
            ) : buckets.length === 0 ? (
              <EmptyState title="No latency samples" />
            ) : (
              <LineChart
                labels={labels}
                formatValue={(value) => formatDurationMs(value)}
                series={[
                  { name: 'p50', color: 'hsl(var(--success))', values: latencies.p50 },
                  { name: 'p95', color: 'hsl(var(--warning))', values: latencies.p95 },
                  { name: 'p99', color: 'hsl(var(--danger))', values: latencies.p99 },
                ]}
              />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Cost by provider</CardTitle>
            <CardDescription>Estimated from registry pricing, which is also what budgets enforce.</CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <TableSkeleton rows={4} columns={1} />
            ) : (
              <BarList
                rows={(data?.providers ?? []).map((row) => ({
                  label: row.provider,
                  value: row.cost_usd,
                  detail: `${formatNumber(row.requests)} req`,
                  tone: row.error_rate > 0.1 ? 'danger' : row.error_rate > 0.02 ? 'warning' : 'default',
                }))}
                formatValue={(value) => formatCurrency(value)}
                emptyMessage="No provider activity in this window"
              />
            )}
          </CardContent>
        </Card>
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Top providers by requests</CardTitle>
            <CardDescription>Requests that were served by each provider, after any failover.</CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <TableSkeleton rows={5} columns={1} />
            ) : (
              <BarList
                rows={(data?.providers ?? []).map((row) => ({
                  label: row.provider,
                  value: row.requests,
                  detail: row.error_rate > 0 ? formatPercent(row.error_rate) : undefined,
                  tone: row.error_rate > 0.1 ? 'danger' : row.error_rate > 0.02 ? 'warning' : 'default',
                }))}
                formatValue={(value) => formatNumber(value)}
                emptyMessage="No provider activity in this window"
              />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Top models by requests</CardTitle>
            <CardDescription>The routed model, which may differ from what the client requested.</CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <TableSkeleton rows={5} columns={1} />
            ) : (
              <BarList
                rows={(data?.models ?? []).map((row) => ({
                  label: row.provider ? `${row.model} · ${row.provider}` : row.model,
                  value: row.requests,
                  detail: formatCurrency(row.cost_usd),
                }))}
                formatValue={(value) => formatNumber(value)}
                emptyMessage="No model activity in this window"
              />
            )}
          </CardContent>
        </Card>
      </div>

      {isFetching && !isPending ? (
        <p className="mt-4 text-center text-xs text-muted-foreground">Refreshing…</p>
      ) : null}
    </>
  );
}

/** Exported so the page can build a permalink that preserves the window. */
export function overviewHref(window: string, tenantId: string): string {
  return `/${buildQuery({ window, tenant: tenantId })}`;
}
