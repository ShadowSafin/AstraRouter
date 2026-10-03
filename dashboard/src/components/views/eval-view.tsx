'use client';

import * as React from 'react';
import { FlaskConical, Play } from 'lucide-react';
import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useCreateReplay, useEvalDetail, useEvalRuns, useReplayJobs } from '@/hooks/use-admin';
import { useTenantParam } from '@/components/tenant-picker';

export function EvalView() {
  const tenantId = useTenantParam();
  const runs = useEvalRuns(tenantId);
  const [selected, setSelected] = React.useState('');
  const detail = useEvalDetail(selected);

  return (
    <>
      <PageHeader title="Evaluations" description="Offline comparisons of provider outputs: latency, cost, quality scores and regression flags." />
      {runs.isError ? <ErrorState error={runs.error} onRetry={() => void runs.refetch()} /> : null}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <FlaskConical className="size-4" />
            Evaluation runs
          </CardTitle>
          <CardDescription>Select a run to inspect per-request scores.</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {runs.isPending ? (
            <TableSkeleton rows={4} columns={4} />
          ) : (runs.data ?? []).length === 0 ? (
            <EmptyState title="No evaluations yet" description="Create a replay job to produce the first evaluation run." className="m-5" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Run</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Dataset</TableHead>
                  <TableHead>Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(runs.data ?? []).map((r) => (
                  <TableRow key={r.id} className={selected === r.id ? 'bg-accent' : undefined} onClick={() => setSelected(r.id)}>
                    <TableCell className="font-mono text-xs">{r.id.slice(0, 8)}</TableCell>
                    <TableCell>
                      <Badge tone={r.status === 'completed' ? 'success' : r.status === 'failed' ? 'danger' : 'info'}>{r.status}</Badge>
                    </TableCell>
                    <TableCell className="text-xs">{r.dataset || '—'}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{new Date(r.created_at).toLocaleString()}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {selected && detail.data ? (
        <Card className="mt-4">
          <CardHeader>
            <CardTitle>Results</CardTitle>
            <CardDescription>
              {detail.data.results?.length ?? 0} scored outputs. Regressions are flagged against golden thresholds.
            </CardDescription>
          </CardHeader>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Provider / model</TableHead>
                  <TableHead className="text-right">Score</TableHead>
                  <TableHead className="text-right">Latency</TableHead>
                  <TableHead className="text-right">Cost</TableHead>
                  <TableHead>Preview</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(detail.data.results ?? []).map((res) => (
                  <TableRow key={res.id}>
                    <TableCell className="text-xs">
                      {res.provider}/{res.model}{' '}
                      {res.is_regression ? <Badge tone="danger">regression</Badge> : null}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{res.score.toFixed(3)}</TableCell>
                    <TableCell className="text-right tabular-nums">{res.latency_ms}ms</TableCell>
                    <TableCell className="text-right tabular-nums">${res.cost_usd.toFixed(4)}</TableCell>
                    <TableCell className="max-w-md truncate text-xs text-muted-foreground">{res.output_preview}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}
    </>
  );
}

export function ReplayView() {
  const tenantId = useTenantParam();
  const { data, isPending, isError, error, refetch } = useReplayJobs(tenantId);
  const create = useCreateReplay();
  const [ids, setIds] = React.useState('');
  const [name, setName] = React.useState('');
  const [providers, setProviders] = React.useState('');
  const [models, setModels] = React.useState('');
  const [createdId, setCreatedId] = React.useState<string | null>(null);

  const splitList = (value: string) =>
    value.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean);

  const onCreate = () => {
    const request_ids = splitList(ids);
    if (request_ids.length === 0) return;
    setCreatedId(null);
    create.mutate(
      {
        name: name.trim() || undefined,
        request_ids,
        providers: splitList(providers),
        models: splitList(models),
        tenant_id: tenantId || undefined,
      },
      {
        onSuccess: (job) => {
          setIds('');
          setCreatedId(job.id);
          void refetch();
        },
      },
    );
  };

  const targetsOf = (j: { providers?: string[]; models?: string[] }) => {
    const p = (j.providers ?? []).filter(Boolean);
    const m = (j.models ?? []).filter(Boolean);
    if (p.length === 0 && m.length === 0) return 'original targets';
    return [...p, ...m].join(', ');
  };

  return (
    <>
      <PageHeader title="Replay jobs" description="Re-execute captured traffic against multiple providers for offline comparison. Jobs run asynchronously in the gateway; this list refreshes every few seconds." />
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Play className="size-4" />
            New replay
          </CardTitle>
          <CardDescription>Paste request IDs from the Requests view, comma or space separated. Only requests with a captured prompt can run — older traffic is rejected with the reason.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <div className="flex items-center gap-2">
            <input
              className="h-9 flex-1 rounded-md border border-input bg-background px-3 font-mono text-sm"
              placeholder="req_abc123, req_def456"
              value={ids}
              onChange={(e) => setIds(e.target.value)}
              spellCheck={false}
            />
            <Button size="sm" onClick={onCreate} disabled={create.isPending || !ids.trim()}>
              {create.isPending ? 'Creating…' : 'Create job'}
            </Button>
          </div>
          <div className="grid gap-2 sm:grid-cols-3">
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="Job name (optional)"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="Providers, comma separated (optional)"
              value={providers}
              onChange={(e) => setProviders(e.target.value)}
              spellCheck={false}
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="Models, comma separated (optional)"
              value={models}
              onChange={(e) => setModels(e.target.value)}
              spellCheck={false}
            />
          </div>
          <p className="text-[11px] text-muted-foreground">
            Leave providers and models empty to replay each request against the target that originally served it.
          </p>
          {create.isError ? <ErrorState error={create.error} onRetry={onCreate} /> : null}
          {createdId && !create.isError ? (
            <p className="text-xs text-emerald-600 dark:text-emerald-400">
              Job {createdId.slice(0, 8)} created — watch its progress below, then inspect per-request outputs on the Evaluations page.
            </p>
          ) : null}
        </CardContent>
      </Card>
      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Jobs</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={4} columns={4} />
          ) : isError ? (
            <ErrorState error={error} onRetry={() => void refetch()} className="m-5" />
          ) : (data ?? []).length === 0 ? (
            <EmptyState title="No replay jobs" description="Replayed traffic will appear here with progress and status." className="m-5" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Job</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Targets</TableHead>
                  <TableHead className="text-right">Progress</TableHead>
                  <TableHead>Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(data ?? []).map((j) => (
                  <TableRow key={j.id}>
                    <TableCell className="font-mono text-xs">{j.id.slice(0, 8)} {j.name ? `· ${j.name}` : ''}</TableCell>
                    <TableCell>
                      <Badge tone={j.status === 'completed' ? 'success' : j.status === 'failed' ? 'danger' : 'info'}>{j.status}</Badge>
                      {j.error ? <p className="text-[11px] text-danger">{j.error}</p> : null}
                    </TableCell>
                    <TableCell className="max-w-48 truncate text-xs text-muted-foreground" title={targetsOf(j)}>{targetsOf(j)}</TableCell>
                    <TableCell className="text-right tabular-nums">{j.progress ?? 0}/{j.total ?? 0}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{new Date(j.created_at).toLocaleString()}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
