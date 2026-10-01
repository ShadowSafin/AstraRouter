import type { TimeBucket } from './types';

/**
 * Chart data layer: typed transforms from API buckets to chart-ready series.
 *
 * Every function here is defensive in the same way: a missing or non-numeric
 * field becomes a zero *and* a warning, so a backend shape change shows up as
 * a debug-panel warning instead of a silently flat chart. The one exception
 * is totals, which read the bucket's own `requests` field rather than
 * re-summing outcome parts — the parts do not partition the whole (canceled
 * requests belong to neither), so recomputing would undercount.
 */

export interface BucketWarning {
  index: number;
  field: string;
  message: string;
}

export interface NumericSeries {
  values: number[];
  warnings: BucketWarning[];
}

/** The numeric bucket fields a chart may legitimately read. */
export const NUMERIC_BUCKET_FIELDS = [
  'requests',
  'successes',
  'errors',
  'rejections',
  'fallbacks',
  'cache_hits',
  'prompt_tokens',
  'completion_tokens',
  'total_tokens',
  'cost_usd',
  'latency_p50_ms',
  'latency_p95_ms',
  'latency_p99_ms',
] as const;

export type NumericBucketField = (typeof NUMERIC_BUCKET_FIELDS)[number];

/**
 * Extract one numeric series, coercing anything that is not a finite number
 * to zero and saying so. `null` and `undefined` buckets cannot happen from
 * the API, but a sparse test fixture may include them.
 */
export function bucketSeries(
  buckets: Array<TimeBucket | null | undefined> | null | undefined,
  field: NumericBucketField,
): NumericSeries {
  const values: number[] = [];
  const warnings: BucketWarning[] = [];
  if (!buckets) return { values, warnings };
  buckets.forEach((bucket, index) => {
    const raw = bucket == null ? undefined : (bucket[field] as unknown);
    if (typeof raw === 'number' && Number.isFinite(raw)) {
      values.push(raw);
      return;
    }
    values.push(0);
    warnings.push({
      index,
      field,
      message: `bucket ${index} has a non-numeric ${field} (${String(raw)}); charted as 0`,
    });
  });
  return { values, warnings };
}

/** Per-bucket request totals, read from the bucket — never recomputed. */
export function bucketTotals(buckets: TimeBucket[] | null | undefined): number[] {
  return bucketSeries(buckets, 'requests').values;
}

/** Per-bucket share of successful requests; an empty bucket reads as a gap. */
export function successShare(buckets: TimeBucket[] | null | undefined): number[] {
  if (!buckets) return [];
  return buckets.map((bucket) => {
    if (!bucket || bucket.requests <= 0) return 0;
    return bucket.successes / bucket.requests;
  });
}

/** Per-bucket cache hit share; an empty bucket reads as a gap. */
export function cacheHitShare(buckets: TimeBucket[] | null | undefined): number[] {
  if (!buckets) return [];
  return buckets.map((bucket) => {
    if (!bucket || bucket.requests <= 0) return 0;
    const hits = typeof bucket.cache_hits === 'number' ? bucket.cache_hits : 0;
    return hits / bucket.requests;
  });
}

export interface OutcomeSplit {
  successes: number[];
  fallbacks: number[];
  errors: number[];
}

/** Requests by outcome, as three aligned series. */
export function outcomeSplit(buckets: TimeBucket[] | null | undefined): OutcomeSplit {
  return {
    successes: bucketSeries(buckets, 'successes').values,
    fallbacks: bucketSeries(buckets, 'fallbacks').values,
    errors: (buckets ?? []).map((bucket) => {
      const errors = bucket?.errors;
      const rejections = bucket?.rejections;
      return (typeof errors === 'number' ? errors : 0) + (typeof rejections === 'number' ? rejections : 0);
    }),
  };
}

export interface LatencySplit {
  p50: number[];
  p95: number[];
  p99: number[];
}

/** Latency percentiles, as three aligned series. */
export function latencySplit(buckets: TimeBucket[] | null | undefined): LatencySplit {
  return {
    p50: bucketSeries(buckets, 'latency_p50_ms').values,
    p95: bucketSeries(buckets, 'latency_p95_ms').values,
    p99: bucketSeries(buckets, 'latency_p99_ms').values,
  };
}

/**
 * Cross-check the summary against the series. When these disagree, one of the
 * two queries is wrong — the dashboard must say so rather than show a summary
 * of 129 next to a chart of zeros.
 */
export function checkSummaryAgreement(summaryRequests: number | undefined, buckets: TimeBucket[] | null | undefined): string | null {
  if (summaryRequests === undefined || !buckets || buckets.length === 0) return null;
  const seriesTotal = buckets.reduce((total, bucket) => {
    const requests = bucket?.requests;
    return total + (typeof requests === 'number' ? requests : 0);
  }, 0);
  if (summaryRequests === 0 && seriesTotal === 0) return null;
  if (summaryRequests === seriesTotal) return null;
  return `summary reports ${summaryRequests} requests but the series sums to ${seriesTotal}; one of the two queries is wrong`;
}

/** Names of fields present on the first bucket, for shape debugging. */
export function bucketFields(buckets: TimeBucket[] | null | undefined): string[] {
  if (!buckets || buckets.length === 0) return [];
  const first = buckets[0];
  if (!first || typeof first !== 'object') return [];
  return Object.keys(first);
}
