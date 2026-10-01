'use client';

import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select, fieldClasses } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useCreateTool, useDeleteTool, useSetToolEnabled, useTools, useUpdateTool } from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import { formatDateTime } from '@/lib/format';
import { cn } from '@/lib/utils';
import type { Tool, ToolInput } from '@/lib/types';

const SAFETY_TONE: Record<string, 'success' | 'warning' | 'danger'> = {
  safe: 'success',
  sensitive: 'warning',
  dangerous: 'danger',
};

const KIND_OPTIONS = ['external', 'builtin'] as const;
const SAFETY_OPTIONS = ['safe', 'sensitive', 'dangerous'] as const;

interface ToolFormState {
  name: string;
  description: string;
  kind: string;
  safety: string;
  handler: string;
  version: string;
  enabled: boolean;
  requiresApproval: boolean;
  parameters: string;
}

function formFromTool(tool?: Tool): ToolFormState {
  return {
    name: tool?.name ?? '',
    description: tool?.description ?? '',
    kind: tool?.kind ?? 'external',
    safety: tool?.safety_level ?? 'safe',
    handler: tool?.handler ?? '',
    version: tool?.version ?? '',
    enabled: tool?.enabled ?? true,
    requiresApproval: tool?.requires_approval ?? false,
    parameters: tool?.parameters ? JSON.stringify(tool.parameters, null, 2) : '{\n  "type": "object",\n  "properties": {}\n}',
  };
}

function toInput(form: ToolFormState): { input: ToolInput; error?: string } {
  let parameters: Record<string, unknown> | undefined;
  const trimmed = form.parameters.trim();
  if (trimmed) {
    try {
      parameters = JSON.parse(trimmed) as Record<string, unknown>;
    } catch (error) {
      return { input: { name: '' }, error: `parameters: ${error instanceof Error ? error.message : 'invalid JSON'}` };
    }
  }
  if (form.kind === 'builtin' && !form.handler.trim()) {
    return { input: { name: '' }, error: 'a builtin tool requires a handler (e.g. now or echo)' };
  }
  return {
    input: {
      name: form.name.trim(),
      description: form.description.trim() || undefined,
      kind: form.kind as ToolInput['kind'],
      safety_level: form.safety as ToolInput['safety_level'],
      handler: form.kind === 'builtin' ? form.handler.trim() || undefined : undefined,
      version: form.version.trim() || undefined,
      enabled: form.enabled,
      requires_approval: form.requiresApproval,
      parameters,
    },
  };
}

function ToolForm({
  initial,
  toolId,
  isEdit,
  onClose,
}: {
  initial: ToolFormState;
  toolId?: string;
  isEdit: boolean;
  onClose: () => void;
}) {
  const createTool = useCreateTool();
  const updateTool = useUpdateTool();
  const [form, setForm] = React.useState<ToolFormState>(initial);
  const [formError, setFormError] = React.useState<string | null>(null);

  const setText =
    (key: 'name' | 'description' | 'kind' | 'safety' | 'handler' | 'version' | 'parameters') =>
    (event: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
      setForm((current) => ({ ...current, [key]: event.target.value }));
    };

  const pending = createTool.isPending || updateTool.isPending;
  const mutationError = createTool.error ?? updateTool.error;

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setFormError(null);
    if (!form.name.trim()) {
      setFormError('name is required — it is what tool_choice and policies match on');
      return;
    }
    const { input, error } = toInput(form);
    if (error) {
      setFormError(error);
      return;
    }
    if (isEdit) {
      if (!toolId) {
        setFormError('the tool id is missing — close and reopen the form');
        return;
      }
      updateTool.mutate({ id: toolId, patch: input }, { onSuccess: onClose });
    } else {
      createTool.mutate(input, { onSuccess: onClose });
    }
  };

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 md:grid-cols-2">
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Name
          <Input value={form.name} onChange={setText('name')} placeholder="weather_lookup" disabled={isEdit} />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Kind
          <Select value={form.kind} onChange={setText('kind')} disabled={isEdit}>
            {KIND_OPTIONS.map((kind) => (
              <option key={kind} value={kind}>
                {kind === 'builtin' ? 'builtin — runs inside the gateway' : 'external — the client runs it'}
              </option>
            ))}
          </Select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground md:col-span-2">
          Description
          <Input
            value={form.description}
            onChange={setText('description')}
            placeholder="What the tool does, in words the model will read"
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Safety level
          <Select value={form.safety} onChange={setText('safety')}>
            {SAFETY_OPTIONS.map((level) => (
              <option key={level} value={level}>
                {level}
              </option>
            ))}
          </Select>
        </label>
        {form.kind === 'builtin' ? (
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Handler
            <Input value={form.handler} onChange={setText('handler')} placeholder="now" disabled={isEdit} />
          </label>
        ) : null}
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Version
          <Input value={form.version} onChange={setText('version')} placeholder="1" />
        </label>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(event) => setForm((current) => ({ ...current, enabled: event.target.checked }))}
            className="size-4 accent-primary"
          />
          Enabled — advertised to models and usable by policy
        </label>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={form.requiresApproval}
            onChange={(event) =>
              setForm((current) => ({ ...current, requiresApproval: event.target.checked }))
            }
            className="size-4 accent-primary"
          />
          Requires approval — every call goes back to the client
        </label>
      </div>
      <label className="flex flex-col gap-1 text-xs text-muted-foreground">
        Parameters (JSON Schema object)
        <textarea
          value={form.parameters}
          onChange={setText('parameters')}
          rows={8}
          spellCheck={false}
          className={cn(fieldClasses, 'font-mono text-xs')}
        />
      </label>
      {formError ? <p className="text-xs text-danger">{formError}</p> : null}
      {mutationError ? (
        <p className="text-xs text-danger">
          {mutationError instanceof ApiError ? mutationError.message : 'The save failed.'}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={pending}>
          {isEdit ? 'Save tool' : 'Register tool'}
        </Button>
      </div>
    </form>
  );
}

export function ToolsView() {
  const { data, isPending, isError, error, refetch, isFetching } = useTools();
  const setEnabled = useSetToolEnabled();
  const deleteTool = useDeleteTool();

  const [showForm, setShowForm] = React.useState(false);
  const [editing, setEditing] = React.useState<Tool | null>(null);
  const [confirmDelete, setConfirmDelete] = React.useState<Tool | null>(null);

  const tools = data ?? [];

  return (
    <>
      <PageHeader
        title="Tool registry"
        description="Every tool the gateway knows: safe built-ins that run here, and external tools that are advertised to models but always executed by the client. Executability is derived from kind, never granted by a form."
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
              Register tool
            </Button>
          </>
        }
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      {!isError ? (
        <Card>
          {isPending ? (
            <TableSkeleton rows={6} columns={6} />
          ) : tools.length === 0 ? (
            <div className="m-5">
              <EmptyState
                title="No tools registered"
                description="The built-in now and echo tools are seeded at startup. Register an external tool here to make it discoverable to models."
              />
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Safety</TableHead>
                  <TableHead>Owner</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tools.map((tool) => (
                  <TableRow key={tool.id}>
                    <TableCell>
                      <div className="font-mono text-xs font-medium">{tool.name}</div>
                      {tool.description ? (
                        <div className="max-w-md truncate text-xs text-muted-foreground" title={tool.description}>
                          {tool.description}
                        </div>
                      ) : null}
                      {tool.requires_approval ? (
                        <Badge tone="warning" className="mt-1">
                          requires approval
                        </Badge>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <Badge tone={tool.kind === 'builtin' ? 'info' : 'neutral'}>
                        {tool.kind}
                        {tool.kind === 'builtin' && tool.executable ? ' · runs here' : ''}
                      </Badge>
                      {tool.handler ? (
                        <div className="mt-1 font-mono text-[11px] text-muted-foreground">handler {tool.handler}</div>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <Badge tone={SAFETY_TONE[tool.safety_level] ?? 'neutral'} dot>{tool.safety_level}</Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {tool.owner}
                      {tool.tenant_id ? (
                        <div className="font-mono text-[11px]" title={tool.tenant_id}>
                          {tool.tenant_id.slice(0, 8)}…
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <Badge tone={tool.enabled ? 'success' : 'neutral'} dot>
                        {tool.enabled ? 'enabled' : 'disabled'}
                      </Badge>
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                      {formatDateTime(tool.updated_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          title={tool.enabled ? 'Disable' : 'Enable'}
                          disabled={setEnabled.isPending}
                          onClick={() => setEnabled.mutate({ id: tool.id, enabled: !tool.enabled })}
                        >
                          {tool.enabled ? 'Disable' : 'Enable'}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          title="Edit"
                          onClick={() => {
                            setEditing(tool);
                            setShowForm(true);
                          }}
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          title="Delete"
                          disabled={tool.kind === 'builtin'}
                          onClick={() => setConfirmDelete(tool)}
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
            <CardTitle>{editing ? `Edit ${editing.name}` : 'Register a tool'}</CardTitle>
            <CardDescription>
              {editing
                ? 'Name, kind and handler are immutable — they are the tool’s identity.'
                : 'External tools are discovery-only: the gateway advertises them and validates their arguments, but never runs them.'}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ToolForm
              initial={formFromTool(editing ?? undefined)}
              toolId={editing?.id}
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
              The registry entry is removed. Runs that already executed keep their history — nothing is
              rewritten.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setConfirmDelete(null)}>
              Keep it
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={deleteTool.isPending}
              onClick={() =>
                deleteTool.mutate(confirmDelete.id, { onSuccess: () => setConfirmDelete(null) })
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
