'use client';

import { Plus, RefreshCw, Users } from 'lucide-react';
import Link from 'next/link';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select, fieldClasses } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useCreateTenant, useDeleteTenant, useTenants, useUpdateTenant } from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import { formatDateTime } from '@/lib/format';
import type { Tenant } from '@/lib/types';

function parseLabels(value: string): { ok: boolean; labels?: Record<string, string>; error?: string } {
  const trimmed = value.trim();
  if (!trimmed) return { ok: true, labels: undefined };
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      return { ok: false, error: 'must be a JSON object' };
    }
    return { ok: true, labels: parsed as Record<string, string> };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : 'invalid JSON' };
  }
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-muted-foreground">
      {label}
      {children}
    </label>
  );
}

function TenantForm({
  initial,
  isEdit,
  tenantId,
  onClose,
}: {
  initial: { slug: string; name: string; status: string; plan: string; labels: string; default_routing_policy_id: string };
  isEdit: boolean;
  tenantId?: string;
  onClose: () => void;
}) {
  const createTenant = useCreateTenant();
  const updateTenant = useUpdateTenant();
  const [form, setForm] = React.useState(initial);
  const [formError, setFormError] = React.useState<string | null>(null);
  const [success, setSuccess] = React.useState<string | null>(null);

  const set =
    (key: keyof typeof form) =>
    (event: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
      setForm((current) => ({ ...current, [key]: event.target.value }));
    };

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setFormError(null);
    setSuccess(null);
    if (!form.slug.trim()) {
      setFormError('slug is required');
      return;
    }
    if (!form.name.trim()) {
      setFormError('name is required');
      return;
    }
    const labels = parseLabels(form.labels);
    if (!labels.ok) {
      setFormError(`labels: ${labels.error}`);
      return;
    }
    const body: Record<string, unknown> = {
      slug: form.slug.trim(),
      name: form.name.trim(),
      status: form.status,
    };
    if (form.plan.trim()) body.plan = form.plan.trim();
    if (labels.labels) body.labels = labels.labels;
    if (form.default_routing_policy_id.trim()) {
      body.default_routing_policy_id = form.default_routing_policy_id.trim();
    }

    if (isEdit && tenantId) {
      updateTenant.mutate(
        { id: tenantId, body },
        {
          onSuccess: () => {
            setSuccess('saved');
            window.setTimeout(onClose, 1000);
          },
          onError: (mutationError) =>
            setFormError(mutationError instanceof ApiError ? mutationError.message : 'the tenant could not be updated'),
        },
      );
    } else {
      createTenant.mutate(body as unknown as Parameters<typeof createTenant.mutate>[0], {
        onSuccess: () => {
          setSuccess('created');
          setForm({ slug: '', name: '', status: 'active', plan: '', labels: '', default_routing_policy_id: '' });
        },
        onError: (mutationError) =>
          setFormError(mutationError instanceof ApiError ? mutationError.message : 'the tenant could not be created'),
      });
    }
  };

  const pending = createTenant.isPending || updateTenant.isPending;

  return (
    <form onSubmit={onSubmit} className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="Slug (immutable)">
          <Input value={form.slug} onChange={set('slug')} placeholder="acme" disabled={isEdit} />
        </Field>
        <Field label="Name">
          <Input value={form.name} onChange={set('name')} placeholder="Acme Inc" />
        </Field>
        <Field label="Status">
          <Select value={form.status} onChange={set('status')}>
            <option value="active">active</option>
            <option value="disabled">disabled</option>
            <option value="degraded">degraded</option>
            <option value="pending">pending</option>
          </Select>
        </Field>
        <Field label="Plan">
          <Input value={form.plan} onChange={set('plan')} placeholder="pro" />
        </Field>
        <Field label="Default routing policy id">
          <Input value={form.default_routing_policy_id} onChange={set('default_routing_policy_id')} placeholder="(platform default)" />
        </Field>
      </div>
      <Field label="Labels (JSON object, optional)">
        <textarea value={form.labels} onChange={set('labels')} placeholder='{"tier": "paid"}' rows={2} className={fieldClasses} />
      </Field>
      {formError ? <p className="text-xs text-danger">{formError}</p> : null}
      {success ? <p className="text-xs text-success">{success}</p> : null}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={pending}>
          {pending ? 'Saving…' : isEdit ? 'Save changes' : 'Create tenant'}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function TenantRow({ tenant }: { tenant: Tenant }) {
  const remove = useDeleteTenant();
  const [editing, setEditing] = React.useState(false);
  const [feedback, setFeedback] = React.useState<string | null>(null);
  const [needsForce, setNeedsForce] = React.useState(false);

  const doDelete = (force: boolean) => {
    const message = force
      ? `Force-delete tenant "${tenant.name}" even though it still has active keys? Its keys stop working immediately.`
      : `Delete tenant "${tenant.name}"?`;
    if (!window.confirm(message)) return;
    setFeedback(null);
    setNeedsForce(false);
    remove.mutate(
      { id: tenant.id, force },
      {
        onSuccess: () => setFeedback('deleted'),
        onError: (e) => {
          if (e instanceof ApiError) {
            setFeedback(e.message);
            // The gateway refuses with a 400 while active keys exist; surface
            // the force option instead of leaving the operator stuck.
            if (e.status === 400) setNeedsForce(true);
          } else {
            setFeedback('delete failed');
          }
        },
      },
    );
  };

  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-2">
          <Users className="size-4 text-muted-foreground" />
          <span className="text-sm font-medium">{tenant.name}</span>
        </div>
        <p className="font-mono text-[11px] text-muted-foreground">{tenant.id}</p>
        {feedback ? <p className="text-[11px] text-info">{feedback}</p> : null}
        {needsForce ? (
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <span className="text-[11px] text-warning">Active keys still exist. Revoke them, or force-delete.</span>
            <Button variant="destructive" size="sm" onClick={() => doDelete(true)} disabled={remove.isPending}>
              Force delete
            </Button>
          </div>
        ) : null}
        {editing ? (
          <div className="mt-2 rounded-md border border-border p-3">
            <TenantForm
              isEdit
              tenantId={tenant.id}
              initial={{
                slug: tenant.slug,
                name: tenant.name,
                status: tenant.status,
                plan: tenant.plan || '',
                labels:
                  tenant.labels && Object.keys(tenant.labels).length > 0 ? JSON.stringify(tenant.labels) : '',
                default_routing_policy_id: tenant.default_routing_policy_id || '',
              }}
              onClose={() => setEditing(false)}
            />
          </div>
        ) : null}
      </TableCell>
      <TableCell className="font-mono text-xs">{tenant.slug}</TableCell>
      <TableCell>
        <Badge tone={tenant.status === 'active' ? 'success' : 'neutral'} dot>
          {tenant.status}
        </Badge>
      </TableCell>
      <TableCell className="text-xs">{tenant.plan || '—'}</TableCell>
      <TableCell className="text-xs">
        {tenant.default_routing_policy_id ? (
          <span className="font-mono">{tenant.default_routing_policy_id}</span>
        ) : (
          <span className="text-muted-foreground">platform default</span>
        )}
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">{formatDateTime(tenant.created_at)}</TableCell>
      <TableCell className="text-right">
        <div className="flex flex-wrap justify-end gap-2">
          <Button asChild variant="outline" size="sm">
            <Link href={`/keys?tenant=${tenant.id}`}>Keys</Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href={`/budgets?tenant=${tenant.id}`}>Budgets</Link>
          </Button>
          <Button variant="outline" size="sm" onClick={() => setEditing((value) => !value)}>
            {editing ? 'Close' : 'Edit'}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => doDelete(false)} disabled={remove.isPending}>
            Delete
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

export function TenantsView() {
  const { data: tenants, isPending, isError, error, refetch, isFetching } = useTenants();
  const [showCreate, setShowCreate] = React.useState(false);

  return (
    <>
      <PageHeader
        title="Tenants"
        description="The top-level ownership boundary. Every credential, usage record and budget belongs to exactly one tenant; disabling a tenant stops all of its keys at once, which is the fastest brake available."
        actions={
          <>
            <Button size="sm" onClick={() => setShowCreate((value) => !value)}>
              <Plus />
              {showCreate ? 'Hide form' : 'Create tenant'}
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
            <CardTitle>Create tenant</CardTitle>
            <CardDescription>The slug is the stable identifier used in API scoping.</CardDescription>
          </CardHeader>
          <CardContent>
            <TenantForm
              isEdit={false}
              initial={{ slug: '', name: '', status: 'active', plan: '', labels: '', default_routing_policy_id: '' }}
              onClose={() => setShowCreate(false)}
            />
          </CardContent>
        </Card>
      ) : null}

      <Card>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={4} columns={5} />
          ) : (tenants ?? []).length === 0 ? (
            <EmptyState
              title="No tenants"
              description="The default tenant is created on first boot from the config's app.default_tenant_slug."
              className="m-5"
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Tenant</TableHead>
                  <TableHead>Slug</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Plan</TableHead>
                  <TableHead>Default policy</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(tenants ?? []).map((tenant) => (
                  <TenantRow key={tenant.id} tenant={tenant} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
