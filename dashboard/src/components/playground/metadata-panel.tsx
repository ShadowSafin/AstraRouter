'use client';

/**
 * Routing and debug metadata for a run.
 *
 * This panel is the reason the Playground exists: an answer that succeeded
 * says nothing about *how* it succeeded, and the decision behind it — which
 * provider was chosen, how many attempts it took, whether a cache entry was
 * used, what the policy decided — is what an operator came to check.
 *
 * It reports exactly what the gateway returned. When debug was off, the
 * decision is absent and the panel says so rather than filling the gap with a
 * plausible-looking reconstruction.
 */
import { Check, Copy, SearchCheck } from 'lucide-react';
import * as React from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { formatDurationMs, formatNumber } from '@/lib/format';
import {
  intentHeaders,
  type PlaygroundConfig,
  type PlaygroundRoute,
  type PlaygroundRun,
  type StreamState,
} from '@/lib/playground';
import { cn } from '@/lib/utils';

function Row({
  label,
  value,
  mono = false,
  title,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
  title?: string;
}) {
  if (value == null || value === '') return null;
  return (
    <div className="flex items-baseline justify-between gap-3 py-1.5">
      <dt className="shrink-0 text-[11px] uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd
        title={title}
        className={cn(
          'truncate text-right text-[12px] text-neutral-100',
          mono && 'font-mono',
        )}
      >
        {value}
      </dd>
    </div>
  );
}

function CopyId({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = React.useState(false);
  if (!value) return null;
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-7"
      title={`Copy ${label}`}
      onClick={() => {
        void navigator.clipboard
          .writeText(value)
          .then(() => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          })
          .catch(() => undefined);
      }}
    >
      {copied ? <Check /> : <Copy />}
      {copied ? 'Copied' : 'Copy'}
    </Button>
  );
}

export function MetadataPanel({
  config,
  run,
  partial,
  running,
}: {
  config: PlaygroundConfig;
  /** The most recent completed primary-lane run, or null before the first. */
  run: PlaygroundRun | null;
  /** Live state for the primary lane while a run is in flight. */
  partial: StreamState;
  running: boolean;
}) {
  const [showRaw, setShowRaw] = React.useState(false);
  const route: PlaygroundRoute = running ? partial.route : (run?.route ?? partial.route);
  const usage = running ? partial.usage : (run?.usage ?? null);
  const decision = route.decision;
  const intent = intentHeaders(config);
  const finishReason = running ? partial.finishReason : (run?.finishReason ?? '');

  const debugOn = config.debug;
  const empty = !running && !run;

  return (
    <div className="space-y-3">
      <div className="rounded-xl border border-white/[0.07] bg-white/[0.015] px-3 py-2">
        <dl className="divide-y divide-white/[0.04]">
          <Row label="Request id" value={route.request_id} mono />
          <Row label="Trace id" value={route.trace_id} mono />
          <Row
            label="Provider"
            value={
              route.provider ? <Badge tone="info">{route.provider}</Badge> : undefined
            }
          />
          <Row label="Model" value={route.model} mono />
          <Row
            label="Attempts"
            value={route.attempts > 0 ? String(route.attempts) : undefined}
          />
          <Row
            label="Fallback"
            value={
              route.fallback_used ? <Badge tone="warning">used</Badge> : undefined
            }
          />
          <Row
            label="Cache"
            value={
              route.latency_ms > 0 || route.request_id ? (
                <Badge tone={route.cache_hit ? 'success' : 'neutral'}>
                  {route.cache_hit ? 'hit' : 'miss'}
                </Badge>
              ) : undefined
            }
          />
          <Row
            label="Latency"
            value={route.latency_ms > 0 ? formatDurationMs(route.latency_ms) : undefined}
          />          <Row label="Finish" value={finishReason || undefined} />
        </dl>
        {route.request_id ? (
          <div className="mt-1 border-t border-white/[0.04] pt-1">
            <CopyId value={route.request_id} label="request id" />
          </div>
        ) : null}
      </div>

      <div className="rounded-xl border border-white/[0.07] bg-white/[0.015] px-3 py-2">
        <dl className="divide-y divide-white/[0.04]">
          <Row
            label="Prompt tokens"
            value={usage ? formatNumber(usage.prompt_tokens) : undefined}
          />
          <Row
            label="Completion tokens"
            value={usage ? formatNumber(usage.completion_tokens) : undefined}
          />
          <Row
            label="Total tokens"
            value={usage ? formatNumber(usage.total_tokens) : undefined}
          />
          {!usage && !empty ? (
            <Row
              label="Usage"
              value={
                <span className="text-muted-foreground">
                  not reported
                  {config.streaming && !config.debug ? ' (ask for it with debug)' : ''}
                </span>
              }
            />
          ) : null}
        </dl>
      </div>

      <div className="rounded-xl border border-white/[0.07] bg-white/[0.015] px-3 py-2">
        <p className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Intent headers
        </p>
        {Object.keys(intent).length === 0 ? (
          <p className="py-1 text-[11px] text-muted-foreground">
            None set — the default policy decides, which is the fastest way to see baseline routing.
          </p>
        ) : (
          <ul className="space-y-1 py-1 font-mono text-[11px] text-neutral-300">
            {Object.entries(intent).map(([name, value]) => (
              <li key={name} className="truncate" title={`${name}: ${value}`}>
                <span className="text-sky-400">{name}</span>
                <span className="text-muted-foreground">: </span>
                {value}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="rounded-xl border border-white/[0.07] bg-white/[0.015] px-3 py-2">
        <div className="flex items-center justify-between gap-2">
          <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            Routing decision
          </p>
          <Button variant="ghost" size="sm" className="h-7" onClick={() => setShowRaw((v) => !v)}>
            <SearchCheck />
            {showRaw ? 'Tree' : 'JSON'}
          </Button>
        </div>

        {decision == null ? (
          <p className="py-2 text-[11px] leading-relaxed text-muted-foreground">
            {empty
              ? 'The gateway returns the full routing decision when debug metadata is on. Run once to populate this.'
              : debugOn
                ? 'No decision in the response yet.'
                : 'Debug metadata is off for this run, so the gateway did not attach a decision. Enable it and re-run — the request is otherwise unchanged.'}
          </p>
        ) : showRaw ? (
          <pre className="max-h-72 overflow-auto rounded-md bg-black/40 p-2 font-mono text-[10px] leading-relaxed text-neutral-300">
            {JSON.stringify(decision, null, 2)}
          </pre>
        ) : (
          <DecisionTree value={collapseDecision(decision)} />
        )}
      </div>
    </div>
  );
}

/**
 * Drop the keys already shown in the summary above from the decision view.
 *
 * The gateway flattens its debug fields into the same block as the correlation
 * ids, so rendering it whole would list request id, cache and latency twice.
 * The raw toggle deliberately does not collapse them — that view is the block as
 * received.
 */
function collapseDecision(value: unknown): unknown {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) return value;
  const shown = new Set([
    'request_id',
    'trace_id',
    'cache_hit',
    'fallback_used',
    'latency_ms',
  ]);
  const entries = Object.entries(value as Record<string, unknown>).filter(
    ([key]) => !shown.has(key),
  );
  return Object.fromEntries(entries);
}

/** Walk the decision object into readable rows, without assuming its shape. */
function DecisionTree({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value == null) return null;

  if (typeof value !== 'object') {
    return (
      <p className="font-mono text-[11px] text-neutral-200">
        {String(value)}
      </p>
    );
  }

  if (Array.isArray(value)) {
    if (value.length === 0) return <p className="text-[11px] text-muted-foreground">none</p>;
    return (
      <ul className="space-y-1">
        {value.map((item, index) => (
          <li key={index} className="flex gap-2">
            <span className="shrink-0 font-mono text-[11px] text-muted-foreground">[{index}]</span>
            <span className="min-w-0 flex-1">
              <DecisionTree value={item} depth={depth + 1} />
            </span>
          </li>
        ))}
      </ul>
    );
  }

  const entries = Object.entries(value as Record<string, unknown>).filter(
    ([, item]) => item != null && item !== '',
  );

  if (entries.length === 0) return null;

  return (
    <dl className="divide-y divide-white/[0.04]">
      {entries.map(([key, item]) => {
        const isObject = typeof item === 'object' && item !== null;
        return (
          <div key={key} className="flex items-baseline gap-3 py-1.5">
            <dt className="w-40 shrink-0 truncate text-[11px] uppercase tracking-wide text-muted-foreground">
              {key}
            </dt>
            <dd className="min-w-0 flex-1 font-mono text-[11px] text-neutral-200">
              {isObject ? (
                <div className="rounded-md bg-white/[0.02] px-2 py-1">
                  <DecisionTree value={item} depth={depth + 1} />
                </div>
              ) : (
                <span className="break-words">
                  {typeof item === 'string' ? item : JSON.stringify(item)}
                </span>
              )}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}
