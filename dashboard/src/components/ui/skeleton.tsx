import { cn } from '@/lib/utils';

/**
 * A loading placeholder.
 *
 * The shimmer uses `animate-pulse` rather than a gradient sweep: it needs no
 * extra keyframes and reads as "loading" at a glance, which is the only job a
 * skeleton has.
 */
export function Skeleton({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('animate-pulse rounded-lg bg-white/[0.04]', className)} {...props} />;
}
