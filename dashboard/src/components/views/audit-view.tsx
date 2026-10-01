'use client';

import { History } from 'lucide-react';
import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useAudit } from '@/hooks/use-admin';

export function AuditView() {
  const { data, isPending, isError, error, refetch } = useAudit(100);

  return (
    <>
      <PageHeader
        title="Audit log"
        description="Append-only control-plane history: policy edits, key rotations, kill switches, budget changes and endpoint overrides."
      />
      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <History className="size-4" />
            Recent events
          </CardTitle>
          <CardDescription>Newest first. Inference traffic is not audited here; spend is in usage records.</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={6} columns={4} />
          ) : (data ?? []).length === 0 ? (
            <EmptyState title="No audit events" description="Administrative actions will appear here." className="m-5" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Action</TableHead>
                  <TableHead>Resource</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>When</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(data ?? []).map((e) => (
                  <TableRow key={e.id}>
                    <TableCell>
                      <Badge tone="outline">{e.action}</Badge>{' '}
                      <span className="text-xs text-muted-foreground">{e.resource}</span>
                    </TableCell>
                    <TableCell className="font-mono text-xs">{e.resource_id.slice(0, 12)}</TableCell>
                    <TableCell className="text-xs">{e.actor_label || e.actor_key_id || '—'}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{new Date(e.created_at).toLocaleString()}</TableCell>
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
