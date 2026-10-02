'use client';

/**
 * Session history of past runs.
 *
 * Kept in memory only: a test prompt may carry production data, and a console
 * should not quietly persist it. The value of the list is that each row can
 * carry its exact configuration back into the composer — reproducing a run
 * precisely is usually more useful than re-typing it.
 */
import { History, RotateCw, Upload } from 'lucide-react';
import * as React from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { EmptyState } from '@/components/ui/state';
import { formatDurationMs, formatRelative } from '@/lib/format';
import type { PlaygroundRun } from '@/lib/playground';
import { cn } from '@/lib/utils';

export function HistoryList({
  runs,
  onLoad,
  onRerun,
  onClear,
  disabled,
}: {
  runs: PlaygroundRun[];
  /** Restore a run's configuration and messages into the composer. */
  onLoad: (run: PlaygroundRun) => void;
  /** Execute a run again with its own configuration. */
  onRerun: (run: PlaygroundRun) => void;
  onClear: () => void;
  disabled: boolean;
}) {
  if (runs.length === 0) {
    return (
      <EmptyState
        title="No runs yet"
        description="Each run is recorded here with the configuration that produced it, so a result can be loaded back into the composer or re-executed."
        className="border-0 bg-transparent p-6"
      />
    );
  }

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between gap-2 border-b border-white/[0.06] px-3 py-2">
        <p className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          <History className="size-3.5" />
          {runs.length} run{runs.length === 1 ? '' : 's'} this session
        </p>
        <Button variant="ghost" size="sm" onClick={onClear} disabled={disabled}>
          Clear
        </Button>
      </div>

      <ul className="divide-y divide-white/[0.05]">
        {runs.map((run) => (
          <li key={run.id} className="px-3 py-2.5">
            <div className="flex items-start gap-2">
              <span
                className={cn(
                  'mt-1.5 size-1.5 shrink-0 rounded-full',
                  run.error ? 'bg-rose-400' : 'bg-emerald-400',
                )}
                aria-hidden
              />
              <div className="min-w-0 flex-1">
                <p className="truncate text-[12px] text-neutral-200" title={run.label}>
                  {run.label}
                </p>
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
                  <span>{formatRelative(new Date(run.startedAt).toISOString())}</span>
                  <span aria-hidden>·</span>
                  <span>{formatDurationMs(run.durationMs)}</span>
                  {run.lane === 'compare' ? <Badge tone="info">compare</Badge> : null}
                  {run.config.streaming ? <Badge tone="neutral">stream</Badge> : null}
                  {run.route.provider ? (
                    <Badge tone="outline">{run.route.provider}</Badge>
                  ) : null}
                  {run.error ? (
                    <Badge tone="danger">
                      {run.error.status > 0 ? `HTTP ${run.error.status}` : run.error.code}
                    </Badge>
                  ) : null}
                </div>
              </div>
            </div>

            <div className="mt-1.5 flex gap-1">
              <Button
                variant="outline"
                size="sm"
                className="h-7 text-[11px]"
                title="Load this run's configuration and messages into the composer"
                disabled={disabled}
                onClick={() => onLoad(run)}
              >
                <Upload />
                Load
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 text-[11px]"
                title="Run it again as-is"
                disabled={disabled}
                onClick={() => onRerun(run)}
              >
                <RotateCw />
                Rerun
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
