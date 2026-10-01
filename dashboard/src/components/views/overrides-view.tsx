'use client';

import { OctagonX, Plus, RefreshCw } from 'lucide-react';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useCreateOverride, useOverrides } from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { Override } from '@/lib/types';

function OverrideRow({ override, onRevoked }: { override: Override; onRevoked: () => void }) {
  const createOverride = useCreateOverride();
  const [feedback, setFeedback] = React.useState<string | null>(null);

  const onRevoke = () => {
    if (!window.confirm(`Revoke this override by writing its inverse?`)) return;
    setFeedback(null);
    createOverride.mutate(
      {
        kind: override.kind,
        target: override.target,
        tenant_id: override.tenant_id,
        enabled: !override.enabled,
        reason: `revocation of ${override.id}`,
      },
      {
        onSuccess: () => {
          setFeedback('revoked — inverse row written');
          onRevoked();
        },
        onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'revocation failed'),
      },
    );
  };

  return (
    <TableRow>
      <TableCell className="font-mono text-xs">{override.id.slice(0, 8)}</TableCell>
      <TableCell>
        <Badge tone="outline">{override.kind}</Badge>
      </TableCell>
      <TableCell className="font-mono text-xs">{override.target || '—'}</TableCell>
      <TableCell>
        <Badge tone={override.enabled ? 'danger' : 'success'}>{override.enabled ? 'enforcing' : 'released'}</Badge>
      </TableCell>
      <TableCell className="max-w-xs text-xs text-muted-foreground">{override.reason || '—'}</TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {override.actor || '—'}
        {override.created_at ? <span className="block">{formatRelative(override.created_at)}</span> : null}
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {override.expires_at ? formatDateTime(override.expires_at) : 'never'}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex flex-col items-end gap-1">
          <Button variant="outline" size="sm" onClick={onRevoke} disabled={createOverride.isPending}>
            <OctagonX />
            {createOverride.isPending ? 'Revoking…' : 'Revoke'}
          </Button>
          {feedback ? <span className="text-[11px] text-info">{feedback}</span> : null}
        </div>
      </TableCell>
    </TableRow>
  );
}

export function OverridesView() {
  const { data: overrides, isPending, isError, error, refetch, isFetching } = useOverrides(100);
  const createOverride = useCreateOverride();
  const [showCreate, setShowCreate] = React.useState(false);
  const [kind, setKind] = React.useState('');
  const [target, setTarget] = React.useState('');
  const [tenantId, setTenantId] = React.useState('');
  const [enabled, setEnabled] = React.useState(true);
  const [reason, setReason] = React.useState('');
  const [expiresAt, setExpiresAt] = React.useState('');
  const [formError, setFormError] = React.useState<string | null>(null);
  const [success, setSuccess] = React.useState<string | null>(null);

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setFormError(null);
    setSuccess(null);
    if (!kind.trim()) {
      setFormError('kind is required (e.g. provider_kill, model_disable)');
      return;
    }
    createOverride.mutate(
      {
        kind: kind.trim(),
        target: target.trim() || undefined,
        tenant_id: tenantId.trim() || undefined,
        enabled,
        reason: reason.trim() || undefined,
        expires_at: expiresAt.trim() || undefined,
      },
      {
        onSuccess: () => {
          setSuccess('override recorded');
          setKind('');
          setTarget('');
          setReason('');
          setExpiresAt('');
        },
        onError: (e) => setFormError(e instanceof ApiError ? e.message : 'the override could not be created'),
      },
    );
  };

  return (
    <>
      <PageHeader
        title="Overrides"
        description="Manual control-plane interventions: kill switches and forced states layered over automatic health. Revocation writes the inverse row rather than deleting history, so the audit trail stays complete."
        actions={
          <>
            <Button size="sm" onClick={() => setShowCreate((value) => !value)}>
              <Plus />
              {showCreate ? 'Hide form' : 'New override'}
            </Button>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={isFetching ? 'animate-spin' : undefined} />
              Refresh
            </Button>
          </>
        }
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      {showCreate ? (
        <Card className="mb-4">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <OctagonX className="size-4" />
              New override
            </CardTitle>
            <CardDescription>
              An override takes effect immediately. To release it, revoke the row — the dashboard writes the inverse
              entry.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={onSubmit} className="space-y-4">
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Kind
                  <Input value={kind} onChange={(event) => setKind(event.target.value)} placeholder="provider_kill" />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Target (optional)
                  <Input value={target} onChange={(event) => setTarget(event.target.value)} placeholder="provider id or model" />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Tenant id (optional)
                  <Input value={tenantId} onChange={(event) => setTenantId(event.target.value)} placeholder="blank = global" />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  State
                  <Select value={enabled ? 'enforcing' : 'released'} onChange={(event) => setEnabled(event.target.value === 'enforcing')}>
                    <option value="enforcing">enforcing</option>
                    <option value="released">released</option>
                  </Select>
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Expires at (RFC3339, optional)
                  <Input value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} placeholder="2027-01-01T00:00:00Z" />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Reason
                  <Input value={reason} onChange={(event) => setReason(event.target.value)} placeholder="incident #123" />
                </label>
              </div>
              {formError ? <p className="text-xs text-danger">{formError}</p> : null}
              {success ? <p className="text-xs text-success">{success}</p> : null}
              <div className="flex gap-2">
                <Button type="submit" size="sm" disabled={createOverride.isPending}>
                  {createOverride.isPending ? 'Saving…' : 'Create override'}
                </Button>
                <Button type="button" variant="ghost" size="sm" onClick={() => setShowCreate(false)}>
                  Cancel
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      ) : null}

      <Card>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={5} columns={6} />
          ) : (overrides ?? []).length === 0 ? (
            <EmptyState
              title="No overrides"
              description="Nothing is manually forced. Overrides appear here when an operator kills, pins or releases a target."
              className="m-5"
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Reason</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(overrides ?? []).map((override) => (
                  <OverrideRow key={override.id} override={override} onRevoked={() => void refetch()} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
