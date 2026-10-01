'use client';

import { Clock, Coins, DatabaseZap, Layers, LineChart as ChartIcon, Zap } from 'lucide-react';
import * as React from 'react';

import { BarList } from '@/components/charts/bar-list';
import { LineChart } from '@/components/charts/time-series';
import { ChartDebug } from '@/components/chart-debug';
import { PageHeader } from '@/components/page-header';
import { RangePicker, useWindowParam, windowToFromParam } from '@/components/range-picker';
import { TenantPicker, useTenantParam } from '@/components/tenant-picker';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ChartSkeleton, CardsSkeleton, EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { StatsBento } from '@/components/ui/stats-bento';
import { WidgetBoard } from '@/components/views/widget-board';
import { useCacheStats, useOverview } from '@/hooks/use-admin';
import { bucketLabels, successRate } from '@/lib/metrics';
import { bucketSeries, latencySplit, outcomeSplit, successShare } from '@/lib/charts';
import {
  formatCompact,
  formatCurrency,
  formatDurationMs,
  formatNumber,
  formatPercent,
} from '@/lib/format';

export function AnalyticsView() {
  const window = useWindowParam();
  const tenantId = useTenantParam();
  const from = windowToFromParam(window);

  const { data, isPending, isError, error, refetch, isFetching } = useOverview(tenantId, from);
  const { data: cache } = useCacheStats();

  const summary = data?.summary;
  const buckets = data?.series ?? [];
  const labels = React.useMemo(() => bucketLabels(buckets), [buckets]);

  const requestsSeries = React.useMemo(() => bucketSeries(buckets, 'requests'), [buckets]);
  const requestTotals = requestsSeries.values;
  const outcomes = React.useMemo(() => outcomeSplit(buckets), [buckets]);
  const latencies = React.useMemo(() => latencySplit(buckets), [buckets]);
  const seriesTotal = requestTotals.reduce((total, value) => total + value, 0);
  // Per-bucket success share feeds the bento sparkline. A bucket with no
  // traffic contributes zero, which reads honestly as a gap.
  const successSpark = React.useMemo(() => successShare(buckets), [buckets]);

  const cacheStats = cache?.stats;

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Analytics</h1>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Deep performance metrics, latency distribution, cache efficiency and spend telemetry.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2.5">
          <TenantPicker value={tenantId} />
          <RangePicker value={window} />
        </div>
      </div>

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      <ChartDebug
        window={window}
        tenantId={tenantId}
        buckets={buckets}
        summaryRequests={summary?.requests}
        seriesTotal={seriesTotal}
        warnings={requestsSeries.warnings}
      />

      {/* KPI bento, every figure from the usage store. */}
      {summary ? (
        <StatsBento
          primary={{
            eyebrow: 'Volume',
            value: formatNumber(summary.requests),
            caption: `${formatNumber(summary.successes)} successful responses in this window.`,
          }}
          trend={{
            label: 'Success rate',
            value: formatPercent(successRate(summary)),
            spark: successSpark,
          }}
          miniA={{ value: formatDurationMs(summary.latency_p95_ms), label: 'p95 latency' }}
          miniB={{ value: formatCurrency(summary.total_cost_usd), label: 'estimated spend', icon: Coins }}
        />
      ) : isPending ? (
        <CardsSkeleton count={4} />
      ) : null}

      {/* Traffic Trend Chart */}
      <Card>
        <CardHeader className="pb-2">
          <div className="flex items-baseline justify-between">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <ChartIcon className="size-4 text-primary" />
                Traffic volume
              </CardTitle>
              <CardDescription>
                Inference requests per hour across all active endpoints and models. Times in UTC.
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="pt-2">
          {isPending ? (
            <ChartSkeleton height={260} />
          ) : buckets.length === 0 ? (
            <EmptyState title="No request data in this window" description="Requests will automatically generate telemetry trends here." />
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

      {/* Latency Distribution & Outome Breakdown */}
      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm font-semibold">
              <Clock className="size-4 text-muted-foreground" />
              Latency percentiles
            </CardTitle>
            <CardDescription>
              End-to-end response time experienced by client applications.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <ChartSkeleton />
            ) : buckets.length === 0 ? (
              <EmptyState title="No latency data available" />
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
            <CardTitle className="flex items-center gap-2 text-sm font-semibold">
              <Zap className="size-4 text-muted-foreground" />
              Outcome breakdown
            </CardTitle>
            <CardDescription>
              Successes, automatic fallbacks, and client-facing errors.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {isPending ? (
              <ChartSkeleton />
            ) : buckets.length === 0 ? (
              <EmptyState title="No outcome data available" />
            ) : (
              <LineChart
                labels={labels}
                formatValue={(value) => formatCompact(value)}
                series={[
                  { name: 'Success', color: 'hsl(var(--success))', values: outcomes.successes },
                  { name: 'Fallback', color: 'hsl(var(--warning))', values: outcomes.fallbacks },
                  { name: 'Errors', color: 'hsl(var(--danger))', values: outcomes.errors },
                ]}
              />
            )}
          </CardContent>
        </Card>
      </div>

      {/* Cache Performance & Cost Breakdown */}
      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm font-semibold">
              <DatabaseZap className="size-4 text-muted-foreground" />
              Cache telemetry
            </CardTitle>
            <CardDescription>
              Hit rate and performance breakdown by caching tier.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {cacheStats ? (
              <div className="space-y-4">
                <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                  <div className="rounded-xl border border-border bg-muted p-3 text-center">
                    <p className="text-[10px] font-semibold uppercase text-muted-foreground">Overall hit rate</p>
                    <p className="mt-1 text-lg font-bold text-success">{formatPercent(cacheStats.hit_rate)}</p>
                  </div>
                  <div className="rounded-xl border border-border bg-muted p-3 text-center">
                    <p className="text-[10px] font-semibold uppercase text-muted-foreground">Exact hits</p>
                    <p className="mt-1 text-lg font-bold text-foreground">{formatNumber(cacheStats.exact_hits)}</p>
                  </div>
                  <div className="rounded-xl border border-border bg-muted p-3 text-center">
                    <p className="text-[10px] font-semibold uppercase text-muted-foreground">Prefix hits</p>
                    <p className="mt-1 text-lg font-bold text-foreground">{formatNumber(cacheStats.prefix_hits)}</p>
                  </div>
                  <div className="rounded-xl border border-border bg-muted p-3 text-center">
                    <p className="text-[10px] font-semibold uppercase text-muted-foreground">Semantic hits</p>
                    <p className="mt-1 text-lg font-bold text-foreground">{formatNumber(cacheStats.semantic_hits)}</p>
                  </div>
                </div>
                <BarList
                  rows={[
                    { label: 'Exact cache hits', value: cacheStats.exact_hits, tone: 'default' },
                    { label: 'Prefix cache hits', value: cacheStats.prefix_hits, tone: 'default' },
                    { label: 'Semantic cache hits', value: cacheStats.semantic_hits, tone: 'default' },
                    { label: 'Exact cache misses', value: cacheStats.exact_misses, tone: 'warning' },
                    { label: 'Bypasses', value: cacheStats.bypasses, tone: 'default' },
                  ]}
                  formatValue={(val) => formatNumber(val)}
                  emptyMessage="No cache operations recorded"
                />
              </div>
            ) : (
              <p className="py-6 text-center text-xs text-muted-foreground">Response cache telemetry unavailable</p>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm font-semibold">
              <Layers className="size-4 text-muted-foreground" />
              Top models by traffic
            </CardTitle>
            <CardDescription>
              Aggregated volume and estimated spend per routed model.
            </CardDescription>
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
                emptyMessage="No model traffic recorded in this window"
              />
            )}
          </CardContent>
        </Card>
      </div>

      {isFetching && !isPending ? (
        <p className="text-center text-xs text-muted-foreground">Refreshing analytics…</p>
      ) : null}

      <div className="mt-6">
        <WidgetBoard />
      </div>
    </div>
  );
}
