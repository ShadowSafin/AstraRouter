import { ArrowDownRight, ArrowUpRight, Minus } from 'lucide-react';
import * as React from 'react';

import { cn } from '@/lib/utils';

export interface StatCardProps {
  label: string;
  /**
   * The figure. A node rather than a string so a value can be animated or
   * annotated in place without losing the card's own typography.
   */
  value: React.ReactNode;
  /** An optional comparison against the previous window. */
  delta?: {
    /** Signed change as a ratio, e.g. 0.12 for +12%. */
    ratio: number | null;
    /** A short label for what the comparison is against. */
    label?: string;
    /**
     * When true, a rising value is bad. Error rate and latency should be red
     * when they go up; throughput should not.
     */
    higherIsWorse?: boolean;
  };
  hint?: string;
  icon?: React.ComponentType<{ className?: string }>;
  tone?: 'default' | 'warning' | 'danger' | 'success';
  className?: string;
}

const TONE_VALUE: Record<NonNullable<StatCardProps['tone']>, string> = {
  default: 'text-foreground',
  warning: 'text-warning',
  danger: 'text-danger',
  success: 'text-success',
};

/**
 * Compute the direction and sentiment of a delta.
 *
 * The two are separate: an increase can be good or bad depending on the metric,
 * and a card that colors "up" green unconditionally gets the latency panel
 * exactly backwards.
 */
function describeDelta(ratio: number, higherIsWorse: boolean) {
  if (Math.abs(ratio) < 0.005) {
    return {
      Icon: Minus,
      badgeClass: 'border-white/[0.07] bg-white/[0.04] text-neutral-400',
      text: 'flat',
    };
  }
  const up = ratio > 0;
  const bad = higherIsWorse ? up : !up;
  return {
    Icon: up ? ArrowUpRight : ArrowDownRight,
    badgeClass: bad
      ? 'border-rose-500/25 bg-rose-500/10 text-rose-400'
      : 'border-emerald-500/25 bg-emerald-500/10 text-emerald-400',
    text: `${up ? '+' : ''}${(ratio * 100).toFixed(1)}%`,
  };
}

export function StatCard({ label, value, delta, hint, icon: Icon, tone = 'default', className }: StatCardProps) {
  const rendered =
    delta && delta.ratio !== null && Number.isFinite(delta.ratio)
      ? describeDelta(delta.ratio, delta.higherIsWorse ?? false)
      : null;

  return (
    <div className={cn('rounded-2xl border border-white/[0.07] bg-card/90 p-5 shadow-sm transition-all hover:border-white/[0.12]', className)}>
      <div className="flex items-start justify-between gap-2">
        <p className="text-[11px] font-semibold uppercase tracking-[0.08em] text-neutral-400">{label}</p>
        {Icon ? <Icon className="size-4 text-neutral-500" /> : null}
      </div>
      <p className={cn('mt-3 text-[26px] font-semibold leading-none tracking-tight tabular-nums text-white', TONE_VALUE[tone])}>{value}</p>
      {rendered ? (
        <div className="mt-2.5 flex items-center gap-1.5 text-xs">
          <span className={cn('inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium', rendered.badgeClass)}>
            <rendered.Icon className="size-3" />
            {rendered.text}
          </span>
          {delta?.label ? <span className="text-[11px] text-neutral-400">vs {delta.label}</span> : null}
        </div>
      ) : null}
      {hint ? <p className="mt-2 text-xs text-neutral-400">{hint}</p> : null}
    </div>
  );
}
