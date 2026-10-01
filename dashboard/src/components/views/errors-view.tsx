'use client';

import { RefreshCw, ShieldAlert } from 'lucide-react';

import { BarList } from '@/components/charts/bar-list';
import { PageHeader } from '@/components/page-header';
import { RangePicker, useWindowParam, windowToFromParam } from '@/components/range-picker';
import { OutcomeBadge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useErrors } from '@/hooks/use-admin';
import { formatDateTime, formatDurationMs, formatNumber, truncateMiddle } from '@/lib/format';

/**
 * Explain what an error code means.
 *
 * The gateway emits normalized codes rather than raw upstream text, so the
 * dashboard can say what actually went wrong instead of echoing a provider's
 * prose. When a code is unknown the raw code is shown rather than hidden.
 */
const CODE_HELP: Record<string, string> = {
  invalid_request: 'The client sent something unusable. Never retried and never failed over.',
  authentication_error: 'The gateway or upstream credentials failed.',
  permission_error: 'The credential is valid but lacks the entitlement or scope required.',
  not_found: 'The requested model or route does not exist.',
  rate_limited: 'A throttle, upstream or gateway-side. Recoverable, so the chain may fail over.',
  quota_exceeded: 'A hard upstream quota or an internal spend ceiling blocked the request.',
  timeout: 'The upstream did not answer inside the policy budget (connect, header or stream idle).',
  upstream_error: 'The provider returned 5xx or a malformed response.',
  context_length_exceeded: 'The prompt exceeded the model context window. Not retryable.',
  content_filtered: 'The provider refused the content on policy grounds.',
  provider_unavailable: 'The gateway judged the provider unhealthy and skipped it rather than calling it.',
  request_canceled: 'The client disconnected mid-request. Not a platform fault.',
  internal_error: 'An unexpected gateway fault. Check the gateway logs for the request id.',
  not_implemented: 'The endpoint is planned but not served by this build.',
};

export function ErrorsView() {
  const window = useWindowParam();
  const from = windowToFromParam(window);
  const { data, isPending, isError, error, refetch, isFetching } = useErrors(from);

  const byCode = Object.entries(data?.by_code ?? {}).sort(([, a], [, b]) => b - a);
  const rows = data?.errors ?? [];

  return (
    <>
      <PageHeader
        title="Errors"
        description="Requests that returned a client-visible failure, grouped by normalized error code. A spike in one code points at one provider or one class of client request."
        actions={
          <>
            <RangePicker value={window} />
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={isFetching ? 'animate-spin' : undefined} />
              Refresh
            </Button>
          </>
        }
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      {isPending ? (
        <TableSkeleton rows={8} columns={6} />
      ) : (
        <>
          <div className="grid gap-4 lg:grid-cols-3">
            <Card>
              <CardHeader>
                <CardTitle>Total failures</CardTitle>
                <CardDescription>Every non-success outcome in the window.</CardDescription>
              </CardHeader>
              <CardContent>
                <p className="flex items-center gap-2 text-3xl font-semibold tabular-nums">
                  <ShieldAlert className="size-6 text-danger" />
                  {formatNumber(data?.total ?? 0)}
                </p>
              </CardContent>
            </Card>

            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle>Failures by code</CardTitle>
                <CardDescription>
                  The first thing to look at: which failure mode is actually dominant.
                </CardDescription>
              </CardHeader>
              <CardContent>
                <BarList
                  rows={byCode.map(([code, count]) => ({
                    label: code,
                    value: count,
                    tone: code === 'internal' || code === 'unavailable' ? 'danger' : 'warning',
                  }))}
                  formatValue={(value) => formatNumber(value)}
                  emptyMessage="No failures in this window — as intended."
                />
              </CardContent>
            </Card>
          </div>

          <Card className="mt-4">
            <CardHeader>
              <CardTitle>Failure detail</CardTitle>
              <CardDescription>Newest first, capped at 100 rows.</CardDescription>
            </CardHeader>
            <CardContent className="p-0">
              {rows.length === 0 ? (
                <EmptyState
                  title="No failures recorded"
                  description="Nothing returned a 4xx or 5xx in this window. A fallback that succeeded is not a failure and will not appear here."
                  className="m-5"
                />
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Time</TableHead>
                      <TableHead>Request</TableHead>
                      <TableHead>Code</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Provider</TableHead>
                      <TableHead>Message</TableHead>
                      <TableHead className="text-right">Latency</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {rows.map((row) => (
                      <TableRow key={row.id || row.request_id}>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {formatDateTime(row.created_at)}
                        </TableCell>
                        <TableCell className="font-mono text-xs" title={row.request_id}>
                          {truncateMiddle(row.request_id, 18)}
                        </TableCell>
                        <TableCell>
                          <div className="space-y-0.5">
                            <span className="font-mono text-xs font-medium">{row.error_code || 'unknown'}</span>
                            <p className="max-w-xs text-[11px] text-muted-foreground">
                              {row.error_code ? (CODE_HELP[row.error_code] ?? 'No description available.') : ''}
                            </p>
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-1">
                            <span className="text-xs tabular-nums">{row.status}</span>
                            <OutcomeBadge outcome={row.outcome} />
                          </div>
                        </TableCell>
                        <TableCell className="text-xs">{row.provider || '—'}</TableCell>
                        <TableCell className="max-w-sm truncate text-xs text-muted-foreground" title={row.error_message}>
                          {row.error_message || '—'}
                        </TableCell>
                        <TableCell className="text-right text-xs tabular-nums">
                          {formatDurationMs(row.latency_ms)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </>
  );
}
