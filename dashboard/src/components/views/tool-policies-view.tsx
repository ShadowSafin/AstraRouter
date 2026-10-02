'use client';

import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useDeleteToolPolicy, useSystem, useToolPolicies, useUpsertToolPolicy } from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import { formatDateTime } from '@/lib/format';
import type { ToolPolicy, ToolPolicyInput } from '@/lib/types';

const MODE_TONE: Record<string, 'success' | 'warning' | 'neutral'> = {
  automatic: 'success',
  manual: 'warning',
  disabled: 'neutral',
};

const MODE_OPTIONS = ['manual', 'automatic', 'disabled'] as const;

interface PolicyFormState {
  name: string;
  tenantId: string;
  enabled: boolean;
  mode: string;
  allowedTools: string;
  deniedTools: string;
  maxSteps: string;
  maxToolCalls: string;
  maxRunSeconds: string;
  requireApproval: boolean;
  blockSensitive: boolean;
}

function formFromPolicy(policy?: ToolPolicy): PolicyFormState {
  return {
    name: policy?.name ?? '',
    tenantId: policy?.tenant_id ?? '',
    enabled: policy?.enabled ?? true,
    mode: policy?.mode ?? 'manual',
    allowedTools: (policy?.allowed_tools ?? []).join(', '),
    deniedTools: (policy?.denied_tools ?? []).join(', '),
    maxSteps: policy ? String(policy.max_steps) : '3',
    maxToolCalls: policy ? String(policy.max_tool_calls) : '8',
    maxRunSeconds: policy ? String(policy.max_run_seconds) : '60',
    requireApproval: policy?.require_approval ?? false,
    blockSensitive: policy?.block_sensitive ?? false,
  };
}

function splitList(raw: string): string[] {
  return raw
    .split(/[,;\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function toInput(form: PolicyFormState): { input: ToolPolicyInput; error?: string } {
  const maxSteps = Number(form.maxSteps);
  const maxToolCalls = Number(form.maxToolCalls);
  const maxRunSeconds = Number(form.maxRunSeconds);
  if (!Number.isInteger(maxSteps) || maxSteps < 1 || maxSteps > 10) {
    return { input: { name: '' }, error: 'max steps must be an integer from 1 to 10' };
  }
  if (!Number.isInteger(maxToolCalls) || maxToolCalls < 1 || maxToolCalls > 64) {
    return { input: { name: '' }, error: 'max tool calls must be an integer from 1 to 64' };
  }
  if (!Number.isInteger(maxRunSeconds) || maxRunSeconds < 1 || maxRunSeconds > 600) {
    return { input: { name: '' }, error: 'max run seconds must be an integer from 1 to 600' };
  }
  return {
    input: {
      name: form.name.trim(),
      tenant_id: form.tenantId.trim() || undefined,
      enabled: form.enabled,
      mode: form.mode as ToolPolicyInput['mode'],
      allowed_tools: splitList(form.allowedTools),
      denied_tools: splitList(form.deniedTools),
      require_approval: form.requireApproval,
      max_steps: maxSteps,
      max_tool_calls: maxToolCalls,
      max_run_seconds: maxRunSeconds,
      block_sensitive: form.blockSensitive,
    },
  };
}

function PolicyForm({
  initial,
  isEdit,
  onClose,
}: {
  initial: PolicyFormState;
  isEdit: boolean;
  onClose: () => void;
}) {
  const upsertPolicy = useUpsertToolPolicy();
  const [form, setForm] = React.useState<PolicyFormState>(initial);
  const [formError, setFormError] = React.useState<string | null>(null);

  const setText =
    (key: 'name' | 'tenantId' | 'mode' | 'allowedTools' | 'deniedTools' | 'maxSteps' | 'maxToolCalls' | 'maxRunSeconds') =>
    (event: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
      setForm((current) => ({ ...current, [key]: event.target.value }));
    };

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setFormError(null);
    if (!form.name.trim()) {
      setFormError('name is required — upserts match on name and tenant');
      return;
    }
    const { input, error } = toInput(form);
    if (error) {
      setFormError(error);
      return;
    }
    upsertPolicy.mutate(input, { onSuccess: onClose });
  };

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 md:grid-cols-2">
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Name
          <Input value={form.name} onChange={setText('name')} placeholder="default" disabled={isEdit} />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Tenant id <span className="opacity-70">(blank is the platform default)</span>
          <Input
            value={form.tenantId}
            onChange={setText('tenantId')}
            placeholder="uuid, or empty"
            disabled={isEdit}
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Mode
          <Select value={form.mode} onChange={setText('mode')}>
            <option value="manual">manual — tools are offered, the client runs them</option>
            <option value="automatic">automatic — safe tools run inside the gateway (needs tools.gateway_execution)</option>
            <option value="disabled">disabled — no tool calling at all</option>
          </Select>
        </label>
        <div className="flex items-end gap-6 pb-1">
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(event) => setForm((current) => ({ ...current, enabled: event.target.checked }))}
              className="size-4 accent-primary"
            />
            Enabled
          </label>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={form.requireApproval}
              onChange={(event) =>
                setForm((current) => ({ ...current, requireApproval: event.target.checked }))
              }
              className="size-4 accent-primary"
            />
            Require approval
          </label>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={form.blockSensitive}
              onChange={(event) =>
                setForm((current) => ({ ...current, blockSensitive: event.target.checked }))
              }
              className="size-4 accent-primary"
            />
            Block on sensitive data
          </label>
        </div>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Allowed tools <span className="opacity-70">(comma separated; blank allows all)</span>
          <Input
            value={form.allowedTools}
            onChange={setText('allowedTools')}
            placeholder="now, echo, weather_*"
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Denied tools <span className="opacity-70">(deny wins over allow)</span>
          <Input value={form.deniedTools} onChange={setText('deniedTools')} placeholder="shell_*" />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Max steps <span className="opacity-70">(model round-trips, 1–10)</span>
          <Input value={form.maxSteps} onChange={setText('maxSteps')} inputMode="numeric" />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Max tool calls <span className="opacity-70">(across the whole run, 1–64)</span>
          <Input value={form.maxToolCalls} onChange={setText('maxToolCalls')} inputMode="numeric" />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Max run seconds <span className="opacity-70">(wall clock, 1–600)</span>
          <Input value={form.maxRunSeconds} onChange={setText('maxRunSeconds')} inputMode="numeric" />
        </label>
      </div>
      {formError ? <p className="text-xs text-danger">{formError}</p> : null}
      {upsertPolicy.error ? (
        <p className="text-xs text-danger">
          {upsertPolicy.error instanceof ApiError ? upsertPolicy.error.message : 'The save failed.'}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={upsertPolicy.isPending}>
          {isEdit ? 'Save policy' : 'Create policy'}
        </Button>
      </div>
    </form>
  );
}

export function ToolPoliciesView() {
  const { data, isPending, isError, error, refetch, isFetching } = useToolPolicies();
  const deletePolicy = useDeleteToolPolicy();
  const system = useSystem();
  const toolsConfig = (system.data?.config?.tools ?? {}) as Record<string, unknown>;
  const gatewayExec = toolsConfig.gateway_execution === true;

  const [showForm, setShowForm] = React.useState(false);
  const [editing, setEditing] = React.useState<ToolPolicy | null>(null);
  const [confirmDelete, setConfirmDelete] = React.useState<ToolPolicy | null>(null);

  const policies = data ?? [];

  return (
    <>
      <PageHeader
        title="Tool policies"
        description="The bounds every gateway-side tool run obeys: which tools may run, how many steps a run may take, and how long it may live. A client can only narrow these per request — it can never widen them."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={isFetching ? 'animate-spin' : undefined} />
              Refresh
            </Button>
            <Button
              size="sm"
              onClick={() => {
                setEditing(null);
                setShowForm(true);
              }}
            >
              <Plus />
              New policy
            </Button>
          </>
        }
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      {!system.isPending && !gatewayExec ? (
        <Card>
          <CardContent className="py-3 text-xs text-muted-foreground">
            Gateway-side execution is disabled (<code className="font-mono">tools.gateway_execution: false</code>):
            automatic requests are clamped to manual and clients run their own tools. Set it to{' '}
            <code className="font-mono">true</code> (or{' '}
            <code className="font-mono">AR_TOOLS_GATEWAY_EXECUTION=true</code>) to let these policies execute
            inside the gateway.
          </CardContent>
        </Card>
      ) : null}

      {!isError ? (
        <Card>
          {isPending ? (
            <TableSkeleton rows={4} columns={6} />
          ) : policies.length === 0 ? (
            <div className="m-5">
              <EmptyState
                title="No tool policies"
                description="Without a stored policy every request runs under the built-in manual default: tools are offered, execution stays client-side. Create a policy here to allow gateway-side runs."
              />
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Mode</TableHead>
                  <TableHead>Tools</TableHead>
                  <TableHead className="text-right">Bounds</TableHead>
                  <TableHead className="text-right">Flags</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {policies.map((policy) => (
                  <TableRow key={policy.id}>
                    <TableCell>
                      <div className="font-mono text-xs font-medium">{policy.name}</div>
                      <div className="flex items-center gap-1">
                        <Badge tone={policy.enabled ? 'success' : 'neutral'}>
                          {policy.enabled ? 'enabled' : 'disabled'}
                        </Badge>
                        {policy.tenant_id ? (
                          <span className="font-mono text-[11px] text-muted-foreground" title={policy.tenant_id}>
                            {policy.tenant_id.slice(0, 8)}…
                          </span>
                        ) : (
                          <span className="text-[11px] text-muted-foreground">platform</span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge tone={MODE_TONE[policy.mode] ?? 'neutral'}>{policy.mode}</Badge>
                    </TableCell>
                    <TableCell className="max-w-xs text-xs">
                      {(policy.allowed_tools ?? []).length > 0 ? (
                        <div>
                          allow <span className="font-mono">{policy.allowed_tools?.join(', ')}</span>
                        </div>
                      ) : (
                        <div className="text-muted-foreground">all allowed</div>
                      )}
                      {(policy.denied_tools ?? []).length > 0 ? (
                        <div>
                          deny <span className="font-mono">{policy.denied_tools?.join(', ')}</span>
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-right text-xs tabular-nums text-muted-foreground">
                      {policy.max_steps} steps · {policy.max_tool_calls} calls · {policy.max_run_seconds}s
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex flex-wrap justify-end gap-1">
                        {policy.require_approval ? <Badge tone="warning">approval</Badge> : null}
                        {policy.block_sensitive ? <Badge tone="warning">no sensitive</Badge> : null}
                      </div>
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                      {formatDateTime(policy.updated_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          title="Edit"
                          onClick={() => {
                            setEditing(policy);
                            setShowForm(true);
                          }}
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          title="Delete"
                          disabled={deletePolicy.isPending}
                          onClick={() => setConfirmDelete(policy)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      ) : null}

      {showForm ? (
        <Card className="mt-4">
          <CardHeader>
            <CardTitle>{editing ? `Edit ${editing.name}` : 'New tool policy'}</CardTitle>
            <CardDescription>
              {editing
                ? 'Name and tenant are the identity — everything else can change. Omitting enabled on save leaves the current value alone.'
                : 'The first policy for a tenant takes effect immediately. Omitting enabled means enabled.'}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <PolicyForm
              initial={formFromPolicy(editing ?? undefined)}
              isEdit={editing != null}
              onClose={() => {
                setShowForm(false);
                setEditing(null);
              }}
            />
          </CardContent>
        </Card>
      ) : null}

      {confirmDelete ? (
        <Card className="mt-4 border-danger/40">
          <CardHeader>
            <CardTitle>Delete {confirmDelete.name}?</CardTitle>
            <CardDescription>
              Requests fall back to the built-in manual default. Past runs keep their history.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setConfirmDelete(null)}>
              Keep it
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={deletePolicy.isPending}
              onClick={() =>
                deletePolicy.mutate(confirmDelete.id, { onSuccess: () => setConfirmDelete(null) })
              }
            >
              Delete
            </Button>
          </CardContent>
        </Card>
      ) : null}
    </>
  );
}
