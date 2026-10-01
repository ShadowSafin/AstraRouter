'use client';

import * as React from 'react';
import { useSearchParams } from 'next/navigation';

import type { BucketWarning } from '@/lib/charts';
import { bucketFields, checkSummaryAgreement } from '@/lib/charts';
import type { TimeBucket } from '@/lib/types';

/**
 * Chart debugging panel, gated behind `?debug=1`.
 *
 * It answers the five questions behind every silent chart failure: what range
 * and tenant were asked for, what the API returned, what the transforms made
 * of it, whether the summary agrees with the series, and which buckets were
 * coerced. Read-only and query-gated, so it never leaks into normal use.
 */
export function ChartDebug({
  window,
  tenantId,
  buckets,
  summaryRequests,
  seriesTotal,
  warnings,
}: {
  window: string;
  tenantId: string;
  buckets: TimeBucket[];
  summaryRequests?: number;
  seriesTotal: number;
  warnings: BucketWarning[];
}) {
  return (
    <React.Suspense fallback={null}>
      <ChartDebugInner
        window={window}
        tenantId={tenantId}
        buckets={buckets}
        summaryRequests={summaryRequests}
        seriesTotal={seriesTotal}
        warnings={warnings}
      />
    </React.Suspense>
  );
}

function ChartDebugInner(props: {
  window: string;
  tenantId: string;
  buckets: TimeBucket[];
  summaryRequests?: number;
  seriesTotal: number;
  warnings: BucketWarning[];
}) {
  const params = useSearchParams();
  if (params.get('debug') !== '1') return null;

  const { window, tenantId, buckets, summaryRequests, seriesTotal, warnings } = props;
  const agreement = checkSummaryAgreement(summaryRequests, buckets);
  const interval = buckets.length > 0 && buckets[0] ? buckets[0].interval : '—';

  const rows: Array<[string, string]> = [
    ['window', window],
    ['tenant', tenantId || '(all)'],
    ['bucket interval', String(interval)],
    ['axis timezone', 'UTC'],
    ['buckets', String(buckets.length)],
    ['series total', String(seriesTotal)],
    ['summary requests', summaryRequests === undefined ? '—' : String(summaryRequests)],
    ['bucket fields', bucketFields(buckets).join(', ') || '—'],
  ];

  return (
    <details
      open
      className="mb-4 rounded-xl border border-warning/40 bg-card p-4 text-xs"
      data-testid="chart-debug"
    >
      <summary className="cursor-pointer font-semibold text-warning">
        Chart debug — query-gated (?debug=1), never shown otherwise
      </summary>
      <dl className="mt-3 grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
        {rows.map(([label, value]) => (
          <div key={label} className="flex gap-2">
            <dt className="shrink-0 font-mono text-muted-foreground">{label}</dt>
            <dd className="break-all font-mono text-foreground">{value}</dd>
          </div>
        ))}
      </dl>
      {agreement ? (
        <p className="mt-3 rounded-lg border border-danger/40 p-2 font-mono text-danger">{agreement}</p>
      ) : (
        <p className="mt-3 font-mono text-success">summary and series agree</p>
      )}
      {warnings.length > 0 ? (
        <ul className="mt-2 space-y-1">
          {warnings.map((warning, index) => (
            <li key={index} className="font-mono text-warning">
              bucket {warning.index} · {warning.field}: {warning.message}
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 font-mono text-muted-foreground">no coerced buckets</p>
      )}
    </details>
  );
}

export default ChartDebug;
