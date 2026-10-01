'use client';

import { RefreshCw, Trophy } from 'lucide-react';
import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useScores } from '@/hooks/use-admin';
import { formatPercent } from '@/lib/format';

/** Render a possibly-absent score without throwing during hydration. */
function formatScore(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '—';
  return value.toFixed(3);
}

/** Render a possibly-absent per-success cost without throwing. */
function formatCostPerSuccess(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '—';
  return `$${value.toFixed(4)}`;
}

export function ScoresView() {
  const { data, isPending, isError, error, refetch, isFetching } = useScores();
  const providers = data?.providers ?? [];
  const models = data?.models ?? [];

  return (
    <>
      <PageHeader
        title="Provider scores"
        description="Explainable rankings from real usage: success rate, latency, cost efficiency and feedback. Scores influence routing order without overriding explicit policy ordering."
        actions={
          <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
            <RefreshCw className={isFetching ? 'animate-spin' : undefined} />
            Refresh
          </Button>
        }
      />
      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Trophy className="size-4" />
            Providers
          </CardTitle>
          <CardDescription>Higher score is better. Blended from success, latency, cost and feedback.</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={5} columns={5} />
          ) : providers.length === 0 ? (
            <EmptyState title="No scores yet" description="Scores appear after traffic flows. The scorer needs observations before it can rank providers." className="m-5" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Provider</TableHead>
                  <TableHead className="text-right">Score</TableHead>
                  <TableHead className="text-right">Success</TableHead>
                  <TableHead className="text-right">Avg latency</TableHead>
                  <TableHead className="text-right">Cost/success</TableHead>
                  <TableHead>Explanation</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {providers.map((p) => (
                  <TableRow key={p.id || p.provider_name}>
                    <TableCell>
                      <span className="text-sm font-medium">{p.provider_name || p.provider_id}</span>
                      <p className="text-[11px] text-muted-foreground tabular-nums">{p.requests} requests · {p.window}</p>
                    </TableCell>
                    <TableCell className="text-right tabular-nums font-medium">{formatScore(p.score)}</TableCell>
                    <TableCell className="text-right tabular-nums">{formatPercent(p.success_rate)}</TableCell>
                    <TableCell className="text-right tabular-nums">{Math.round(p.avg_latency_ms)}ms</TableCell>
                    <TableCell className="text-right tabular-nums">{formatCostPerSuccess(p.cost_per_success)}</TableCell>
                    <TableCell className="max-w-md text-xs text-muted-foreground">{p.explanation}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Models</CardTitle>
          <CardDescription>Per-model quality within the same scoring window.</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isPending ? null : models.length === 0 ? (
            <EmptyState title="No model scores" description="Model scores appear alongside provider scores." className="m-5" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Model</TableHead>
                  <TableHead className="text-right">Score</TableHead>
                  <TableHead className="text-right">Success</TableHead>
                  <TableHead>Explanation</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {models.map((m) => (
                  <TableRow key={m.id}>
                    <TableCell>
                      <span className="text-sm font-medium">{m.model}</span>
                      {m.provider_name ? <Badge tone="outline">{m.provider_name}</Badge> : null}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{formatScore(m.score)}</TableCell>
                    <TableCell className="text-right tabular-nums">{formatPercent(m.success_rate)}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{m.explanation}</TableCell>
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
