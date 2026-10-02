import { describe, expect, it } from 'vitest';

import { bucketLabels } from './metrics';
import type { TimeBucket } from './types';
import {
  bucketFields,
  bucketSeries,
  bucketTotals,
  cacheHitShare,
  checkSummaryAgreement,
  latencySplit,
  outcomeSplit,
  successShare,
} from './charts';

function bucket(partial: Partial<TimeBucket> = {}): TimeBucket {
  return {
    start: '2026-10-01T00:00:00Z',
    end: '2026-10-01T01:00:00Z',
    interval: '1h',
    requests: 0,
    successes: 0,
    errors: 0,
    rejections: 0,
    fallbacks: 0,
    cache_hits: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    total_tokens: 0,
    cost_usd: 0,
    latency_p50_ms: 0,
    latency_p90_ms: 0,
    latency_p95_ms: 0,
    latency_p99_ms: 0,
    ...partial,
  };
}

describe('bucketSeries', () => {
  it('extracts values in order', () => {
    const { values, warnings } = bucketSeries(
      [bucket({ requests: 3 }), bucket({ requests: 7 })],
      'requests',
    );
    expect(values).toEqual([3, 7]);
    expect(warnings).toEqual([]);
  });

  it('charts garbage as zero and warns instead of failing silently', () => {
    const broken = bucket({ requests: 3 });
    (broken as unknown as Record<string, unknown>).requests = 'lots';
    const { values, warnings } = bucketSeries([broken], 'requests');
    expect(values).toEqual([0]);
    expect(warnings).toHaveLength(1);
    expect(warnings[0]?.field).toBe('requests');
  });

  it('handles empty input without warnings', () => {
    expect(bucketSeries(null, 'requests')).toEqual({ values: [], warnings: [] });
    expect(bucketSeries([], 'requests')).toEqual({ values: [], warnings: [] });
  });
});

describe('bucketTotals', () => {
  it('reads the bucket requests field rather than recomputing parts', () => {
    // Canceled requests belong to no outcome part: a recomputed sum of
    // successes + errors + rejections + fallbacks would read 4 here.
    const buckets = [bucket({ requests: 5, successes: 3, fallbacks: 1 })];
    expect(bucketTotals(buckets)).toEqual([5]);
  });
});

describe('successShare', () => {
  it('divides successes by requests, with empty buckets as gaps', () => {
    expect(successShare([bucket({ requests: 4, successes: 3 }), bucket()])).toEqual([0.75, 0]);
  });
});

describe('cacheHitShare', () => {
  it('divides cache hits by requests', () => {
    expect(cacheHitShare([bucket({ requests: 10, cache_hits: 4 })])).toEqual([0.4]);
  });
});

describe('outcomeSplit', () => {
  it('combines errors and rejections into one series', () => {
    const split = outcomeSplit([bucket({ successes: 2, fallbacks: 1, errors: 1, rejections: 2 })]);
    expect(split).toEqual({ successes: [2], fallbacks: [1], errors: [3] });
  });
});

describe('latencySplit', () => {
  it('keeps the four percentiles aligned', () => {
    const split = latencySplit([
      bucket({ latency_p50_ms: 100, latency_p90_ms: 300, latency_p95_ms: 500, latency_p99_ms: 900 }),
      bucket({ latency_p50_ms: 120, latency_p90_ms: 340, latency_p95_ms: 600, latency_p99_ms: 1000 }),
    ]);
    expect(split).toEqual({
      p50: [100, 120],
      p90: [300, 340],
      p95: [500, 600],
      p99: [900, 1000],
    });
  });
});

describe('checkSummaryAgreement', () => {
  it('is silent when both are empty or agree', () => {
    expect(checkSummaryAgreement(0, [])).toBeNull();
    expect(checkSummaryAgreement(5, [bucket({ requests: 2 }), bucket({ requests: 3 })])).toBeNull();
  });

  it('reports the production failure mode: a summary with an empty series', () => {
    // This is the bucket-misalignment bug: the summary said 129 while every
    // series bucket read zero.
    const buckets = [bucket(), bucket(), bucket()];
    const message = checkSummaryAgreement(129, buckets);
    expect(message).toContain('129');
    expect(message).toContain('0');
  });
});

describe('bucketFields', () => {
  it('lists the first bucket keys for shape debugging', () => {
    expect(bucketFields([bucket()])).toContain('requests');
    expect(bucketFields([])).toEqual([]);
    expect(bucketFields(null)).toEqual([]);
  });
});

describe('bucketLabels', () => {
  it('renders axis labels in UTC, independent of viewer timezone', () => {
    // A 04:00 UTC bucket must read 04:00 even on a machine at UTC+5:30, where
    // a local rendering would print a misleading 09:30.
    const labels = bucketLabels([bucket({ start: '2026-10-01T04:00:00Z', interval: '1h' })]);
    expect(labels[0]).toContain('04:00');
  });

  it('renders day buckets without a time of day', () => {
    const labels = bucketLabels([bucket({ start: '2026-10-01T00:00:00Z', interval: '1d' })]);
    expect(labels[0]).toMatch(/Oct/);
    expect(labels[0]).not.toContain(':');
  });
});
