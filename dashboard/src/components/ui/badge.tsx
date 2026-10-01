import { cva, type VariantProps } from 'class-variance-authority';
import * as React from 'react';

import { cn } from '@/lib/utils';
import type { HealthState, UsageOutcome } from '@/lib/types';

const badgeVariants = cva(
  'inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-medium transition-colors',
  {
    variants: {
      tone: {
        neutral: 'border-white/[0.08] bg-white/[0.04] text-neutral-400',
        success: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-400',
        warning: 'border-amber-500/25 bg-amber-500/10 text-amber-400',
        danger: 'border-rose-500/25 bg-rose-500/10 text-rose-400',
        info: 'border-sky-500/25 bg-sky-500/10 text-sky-400',
        outline: 'border-white/[0.1] bg-transparent text-neutral-200',
      },
    },
    defaultVariants: { tone: 'neutral' },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {
  dot?: boolean;
}

export function Badge({ className, tone, dot = false, children, ...props }: BadgeProps) {
  const dotColor =
    tone === 'success'
      ? 'bg-emerald-400'
      : tone === 'warning'
        ? 'bg-amber-400'
        : tone === 'danger'
          ? 'bg-rose-400'
          : tone === 'info'
            ? 'bg-sky-400'
            : 'bg-neutral-400';

  return (
    <span className={cn(badgeVariants({ tone }), className)} {...props}>
      {dot ? (
        <span className={cn('mr-1.5 inline-block size-1.5 rounded-full', dotColor)} />
      ) : null}
      {children}
    </span>
  );
}

/**
 * Render a provider health state with a clear glowing dot indicator.
 */
export function HealthBadge({ state }: { state: HealthState | undefined }) {
  const tone =
    state === 'healthy'
      ? 'success'
      : state === 'degraded'
        ? 'warning'
        : state === 'unhealthy'
          ? 'danger'
          : 'neutral';
  return (
    <Badge tone={tone} dot>
      {state ?? 'unknown'}
    </Badge>
  );
}

const OUTCOME_TONE: Record<UsageOutcome, BadgeProps['tone']> = {
  success: 'success',
  fallback: 'warning',
  error: 'danger',
  rejected: 'danger',
  canceled: 'neutral',
};

export function OutcomeBadge({ outcome }: { outcome: UsageOutcome | undefined }) {
  if (!outcome) return <Badge tone="neutral" dot>unknown</Badge>;
  return (
    <Badge tone={OUTCOME_TONE[outcome] ?? 'neutral'} dot>
      {outcome}
    </Badge>
  );
}
