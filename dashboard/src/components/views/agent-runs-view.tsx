'use client';

import { ChevronDown, RefreshCw } from 'lucide-react';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useAgentRunDetail, useAgentRuns, useToolInvocations } from '@/hooks/use-admin';
import { formatDateTime, formatDurationMs, formatNumber, truncateMiddle } from '@/lib/format';
import { cn } from '@/lib/utils';
import type { AgentRun, AgentRunStatus, AgentStep } from '@/lib/types';

const RUN_TONE: Record<AgentRunStatus, 'success' | 'warning' | 'danger' | 'neutral' | 'info'> = {
  completed: 'success',
  running: 'info',
  step_limit: 'warning',
  call_limit: 'warning',
  time_limit: 'warning',
  failed: 'danger',
  denied: 'danger',
};

function StepTrace({ runId }: { runId: string }) {
  const { data, isPending, isError } = useAgentRunDetail(runId);

  if (isPending) return <TableSkeleton rows={3} columns={5} />;
  if (isError) return <p className="p-4 text-xs text-danger">The step trace could not be loaded.</p>;

  const steps: AgentStep[] = data?.steps ?? [];
  if (steps.length === 0) {
    return <p className="p-4 text-xs text-muted-foreground">No steps were recorded for this run.</p>;
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Step</TableHead>
          <TableHead>Kind</TableHead>
          <TableHead>Detail</TableHead>
          <TableHead className="text-right">Latency</TableHead>
          <TableHead className="text-right">Tokens</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {steps.map((step) => (
          <TableRow key={step.id}>
            <TableCell className="font-mono text-xs tabular-nums">{step.step}</TableCell>
            <TableCell>
              <Badge tone={step.kind === 'tool' ? 'info' : 'neutral'}>{step.kind}</Badge>
              {step.tool_calls > 0 ? (
                <span className="ml-1 text-[11px] text-muted-foreground">{step.tool_calls} calls</span>
              ) : null}
            </TableCell>
            <TableCell className="max-w-xl text-xs">
              {step.provider || step.model ? (
                <div className="font-medium">
                  {step.model ?? step.provider}
                  {step.model && step.provider ? (
                    <span className="font-normal text-muted-foreground"> · {step.provider}</span>
                  ) : null}
                </div>
              ) : null}
              {step.detail ? (
                <pre className="mt-1 max-h-32 overflow-auto rounded bg-muted/60 p-2 font-mono text-[11px] text-muted-foreground">
                  {JSON.stringify(step.detail, null, 1)}
                </pre>
              ) : null}
            </TableCell>
            <TableCell className="text-right text-xs tabular-nums">{formatDurationMs(step.latency_ms)}</TableCell>
            <TableCell className="text-right text-xs tabular-nums">{formatNumber(step.tokens)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function InvocationHistory({ runId }: { runId: string }) {
  const { data, isPending, isError } = useToolInvocations(200);
  const invocations = (data?.invocations ?? []).filter((invocation) => invocation.run_id === runId);

  if (isPending) return <TableSkeleton rows={2} columns={5} />;
  if (isError) return <p className="p-4 text-xs text-danger">The invocation history could not be loaded.</p>;
  if (invocations.length === 0) {
    return <p className="p-4 text-xs text-muted-foreground">No tool invocations were recorded for this run.</p>;
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Step</TableHead>
          <TableHead>Tool</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Arguments</TableHead>
          <TableHead className="text-right">Latency</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {invocations.map((invocation) => (
          <TableRow key={invocation.id}>
            <TableCell className="font-mono text-xs tabular-nums">{invocation.step}</TableCell>
            <TableCell className="font-mono text-xs">{invocation.tool_name}</TableCell>
            <TableCell>
              <Badge
                tone={
                  invocation.status === 'executed'
                    ? 'success'
                    : invocation.status === 'pending'
                      ? 'neutral'
                      : invocation.status === 'skipped'
                        ? 'warning'
                        : 'danger'
                }
              >
                {invocation.status}
              </Badge>
              {invocation.deny_reason ? (
                <div className="mt-1 max-w-xs text-[11px] text-muted-foreground">{invocation.deny_reason}</div>
              ) : null}
              {invocation.error_code ? (
                <div className="mt-1 font-mono text-[11px] text-danger">{invocation.error_code}</div>
              ) : null}
            </TableCell>
            <TableCell className="max-w-md text-xs">
              {invocation.arguments ? (
                <pre className="max-h-24 overflow-auto rounded bg-muted/60 p-2 font-mono text-[11px]">
                  {invocation.arguments}
                </pre>
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </TableCell>
            <TableCell className="text-right text-xs tabular-nums">
              {formatDurationMs(invocation.latency_ms)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function RunRow({ run }: { run: AgentRun }) {
  const [open, setOpen] = React.useState(false);

  return (
    <>
      <TableRow className="cursor-pointer" onClick={() => setOpen((value) => !value)}>
        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
          {formatDateTime(run.created_at)}
        </TableCell>
        <TableCell className="whitespace-nowrap font-mono text-xs" title={run.id}>
          {truncateMiddle(run.id, 16)}
        </TableCell>
        <TableCell>
          <Badge tone={RUN_TONE[run.status] ?? 'neutral'}>{run.status}</Badge>
          {run.stop_reason ? (
            <div className="mt-1 line-clamp-2 max-w-xs text-[11px] text-muted-foreground" title={run.stop_reason}>
              {run.stop_reason}
            </div>
          ) : null}
        </TableCell>
        <TableCell className="text-xs">
          <div className="font-medium">{run.model ?? '—'}</div>
          {run.provider ? <div className="text-muted-foreground">{run.provider}</div> : null}
        </TableCell>
        <TableCell className="text-right text-xs tabular-nums">
          {run.steps} steps · {run.tool_calls} calls
        </TableCell>
        <TableCell className="text-right text-xs tabular-nums">{formatDurationMs(run.latency_ms)}</TableCell>
        <TableCell className="text-right">
          <ChevronDown className={cn('size-4 transition-transform', open && 'rotate-180')} />
        </TableCell>
      </TableRow>
      {open ? (
        <TableRow>
          <TableCell colSpan={7} className="bg-muted/30 p-0">
            <div className="space-y-4 p-4">
              <div>
                <p className="mb-2 px-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Step trace
                </p>
                <StepTrace runId={run.id} />
              </div>
              <div>
                <p className="mb-2 px-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Tool invocations
                </p>
                <InvocationHistory runId={run.id} />
              </div>
              <p className="px-1 font-mono text-[11px] text-muted-foreground" title={run.request_id}>
                request {run.request_id}
              </p>
            </div>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

export function AgentRunsView() {
  const { data, isPending, isError, error, refetch, isFetching } = useAgentRuns(100);
  const runs = data?.runs ?? [];

  return (
    <>
      <PageHeader
        title="Agent runs"
        description="Every bounded multi-step tool run the gateway executed: what the model asked for, what ran, what was skipped or denied, and why the run stopped. Expand a row for the full step trace."
        actions={
          <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
            <RefreshCw className={isFetching ? 'animate-spin' : undefined} />
            Refresh
          </Button>
        }
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      {!isError ? (
        <Card>
          {isPending ? (
            <TableSkeleton rows={8} columns={7} />
          ) : runs.length === 0 ? (
            <div className="m-5">
              <EmptyState
                title="No agent runs yet"
                description="Runs appear here when a model calls tools under a policy that allows gateway-side execution. Until then every tool call is executed by the client."
              />
            </div>
          ) : (
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Time</TableHead>
                    <TableHead>Run</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead className="text-right">Work</TableHead>
                    <TableHead className="text-right">Latency</TableHead>
                    <TableHead>
                      <span className="sr-only">Expand</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {runs.map((run) => (
                    <RunRow key={run.id} run={run} />
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          )}
        </Card>
      ) : null}
    </>
  );
}
