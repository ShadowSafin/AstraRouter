'use client';

import { Boxes, Plus, RefreshCw, Search, Trash2 } from 'lucide-react';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select, fieldClasses } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useCreateModel, useDeleteModel, useModels, usePatchModel, useProviders, useUpdateModel } from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import { formatCompact, formatCurrency, formatNumber } from '@/lib/format';
import type { Model } from '@/lib/types';

const STATUS_TONE: Record<string, 'success' | 'warning' | 'neutral' | 'danger'> = {
  active: 'success',
  degraded: 'warning',
  deprecated: 'neutral',
  disabled: 'danger',
};

const STATUS_OPTIONS = ['active', 'degraded', 'deprecated', 'disabled'];
const ENV_OPTIONS = ['', 'production', 'internal', 'external', 'test'];

function parseCsv(value: string): string[] {
  return value
    .split(',')
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
}

function parseOptionalNumber(value: string): number | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : undefined;
}

interface ModelFormState {
  provider_id: string;
  name: string;
  aliases: string;
  display_name: string;
  context_window: string;
  max_output_tokens: string;
  capabilities: string;
  input_cost_per_million: string;
  output_cost_per_million: string;
  cached_input_cost_per_million: string;
  status: string;
  quality_tier: string;
  rate_limit_rpm: string;
  rate_limit_tpm: string;
  priority: string;
  environment: string;
  metadata: string;
}

function emptyModelForm(defaultProviderId = ''): ModelFormState {
  return {
    provider_id: defaultProviderId,
    name: '',
    aliases: '',
    display_name: '',
    context_window: '',
    max_output_tokens: '',
    capabilities: '',
    input_cost_per_million: '',
    output_cost_per_million: '',
    cached_input_cost_per_million: '',
    status: 'active',
    quality_tier: '',
    rate_limit_rpm: '',
    rate_limit_tpm: '',
    priority: '',
    environment: '',
    metadata: '',
  };
}

function formFromModel(model: Model): ModelFormState {
  return {
    provider_id: model.provider_id,
    name: model.name,
    aliases: (model.aliases ?? []).join(', '),
    display_name: model.display_name || '',
    context_window: model.context_window > 0 ? String(model.context_window) : '',
    max_output_tokens: model.max_output_tokens > 0 ? String(model.max_output_tokens) : '',
    capabilities: (model.capabilities ?? []).join(', '),
    input_cost_per_million: String(model.input_cost_per_million ?? ''),
    output_cost_per_million: String(model.output_cost_per_million ?? ''),
    cached_input_cost_per_million:
      model.cached_input_cost_per_million !== undefined ? String(model.cached_input_cost_per_million) : '',
    status: model.status || 'active',
    quality_tier: model.quality_tier !== undefined ? String(model.quality_tier) : '',
    rate_limit_rpm: model.rate_limit_rpm !== undefined ? String(model.rate_limit_rpm) : '',
    rate_limit_tpm: model.rate_limit_tpm !== undefined ? String(model.rate_limit_tpm) : '',
    priority: model.priority !== undefined ? String(model.priority) : '',
    environment: model.environment || '',
    metadata: model.metadata && Object.keys(model.metadata).length > 0 ? JSON.stringify(model.metadata) : '',
  };
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-muted-foreground">
      {label}
      {children}
    </label>
  );
}

function ModelForm({
  initial,
  isEdit,
  modelId,
  onClose,
}: {
  initial: ModelFormState;
  isEdit: boolean;
  modelId?: string;
  onClose: () => void;
}) {
  const { data: providers } = useProviders();
  const createModel = useCreateModel();
  const updateModel = useUpdateModel();
  const [form, setForm] = React.useState<ModelFormState>(initial);
  const [formError, setFormError] = React.useState<string | null>(null);
  const [success, setSuccess] = React.useState<string | null>(null);

  const set =
    (key: keyof ModelFormState) =>
    (event: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
      setForm((current) => ({ ...current, [key]: event.target.value }));
    };

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setFormError(null);
    setSuccess(null);

    if (!form.provider_id) {
      setFormError('select a provider — a model always belongs to one');
      return;
    }
    if (!form.name.trim()) {
      setFormError('name is required');
      return;
    }
    let metadata: Record<string, string> | undefined;
    if (form.metadata.trim()) {
      try {
        const parsed = JSON.parse(form.metadata) as unknown;
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          setFormError('metadata must be a JSON object');
          return;
        }
        metadata = parsed as Record<string, string>;
      } catch (error) {
        setFormError(`metadata: ${error instanceof Error ? error.message : 'invalid JSON'}`);
        return;
      }
    }

    const numbers: Record<string, number | undefined> = {
      context_window: parseOptionalNumber(form.context_window),
      max_output_tokens: parseOptionalNumber(form.max_output_tokens),
      input_cost_per_million: parseOptionalNumber(form.input_cost_per_million),
      output_cost_per_million: parseOptionalNumber(form.output_cost_per_million),
      cached_input_cost_per_million: parseOptionalNumber(form.cached_input_cost_per_million),
      quality_tier: parseOptionalNumber(form.quality_tier),
      rate_limit_rpm: parseOptionalNumber(form.rate_limit_rpm),
      rate_limit_tpm: parseOptionalNumber(form.rate_limit_tpm),
      priority: parseOptionalNumber(form.priority),
    };
    for (const [key, value] of Object.entries(numbers)) {
      const raw = form[key as keyof ModelFormState];
      if (typeof raw === 'string' && raw.trim() && value === undefined) {
        setFormError(`${key} must be a number`);
        return;
      }
    }

    const body: Record<string, unknown> = {
      provider_id: form.provider_id,
      name: form.name.trim(),
      status: form.status,
    };
    const aliases = parseCsv(form.aliases);
    if (aliases.length > 0) body.aliases = aliases;
    if (form.display_name.trim()) body.display_name = form.display_name.trim();
    const capabilities = parseCsv(form.capabilities);
    if (capabilities.length > 0) body.capabilities = capabilities;
    for (const [key, value] of Object.entries(numbers)) {
      if (value !== undefined) body[key] = value;
    }
    if (form.environment.trim()) body.environment = form.environment.trim();
    if (metadata) body.metadata = metadata;

    if (isEdit && modelId) {
      updateModel.mutate(
        { id: modelId, body },
        {
          onSuccess: () => {
            setSuccess('saved');
            window.setTimeout(onClose, 1000);
          },
          onError: (mutationError) =>
            setFormError(mutationError instanceof ApiError ? mutationError.message : 'the model could not be updated'),
        },
      );
    } else {
      createModel.mutate(body as unknown as Parameters<typeof createModel.mutate>[0], {
        onSuccess: () => {
          setSuccess('created');
          setForm(emptyModelForm(form.provider_id));
        },
        onError: (mutationError) =>
          setFormError(mutationError instanceof ApiError ? mutationError.message : 'the model could not be created'),
      });
    }
  };

  const pending = createModel.isPending || updateModel.isPending;

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="Provider">
          <Select value={form.provider_id} onChange={set('provider_id')} disabled={isEdit}>
            <option value="">Select a provider</option>
            {(providers ?? []).map((provider) => (
              <option key={provider.id} value={provider.id}>
                {provider.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Name (immutable with provider)">
          <Input value={form.name} onChange={set('name')} placeholder="gpt-4o" disabled={isEdit} />
        </Field>
        <Field label="Display name">
          <Input value={form.display_name} onChange={set('display_name')} placeholder="GPT-4o" />
        </Field>
        <Field label="Aliases (comma-separated)">
          <Input value={form.aliases} onChange={set('aliases')} placeholder="gpt4o, gpt-4o-latest" />
        </Field>
        <Field label="Context window">
          <Input value={form.context_window} onChange={set('context_window')} placeholder="128000" inputMode="numeric" />
        </Field>
        <Field label="Max output tokens">
          <Input value={form.max_output_tokens} onChange={set('max_output_tokens')} placeholder="4096" inputMode="numeric" />
        </Field>
        <Field label="Capabilities (comma-separated)">
          <Input value={form.capabilities} onChange={set('capabilities')} placeholder="chat, vision" />
        </Field>
        <Field label="Input $/M">
          <Input value={form.input_cost_per_million} onChange={set('input_cost_per_million')} placeholder="2.5" inputMode="decimal" />
        </Field>
        <Field label="Output $/M">
          <Input value={form.output_cost_per_million} onChange={set('output_cost_per_million')} placeholder="10" inputMode="decimal" />
        </Field>
        <Field label="Cached input $/M">
          <Input value={form.cached_input_cost_per_million} onChange={set('cached_input_cost_per_million')} placeholder="1.25" inputMode="decimal" />
        </Field>
        <Field label="Status">
          <Select value={form.status} onChange={set('status')}>
            {STATUS_OPTIONS.map((status) => (
              <option key={status} value={status}>
                {status}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Environment">
          <Select value={form.environment} onChange={set('environment')}>
            {ENV_OPTIONS.map((env) => (
              <option key={env} value={env}>
                {env === '' ? '(none)' : env}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Priority">
          <Input value={form.priority} onChange={set('priority')} placeholder="100" inputMode="numeric" />
        </Field>
        <Field label="Quality tier">
          <Input value={form.quality_tier} onChange={set('quality_tier')} placeholder="1" inputMode="numeric" />
        </Field>
        <Field label="Rate limit RPM">
          <Input value={form.rate_limit_rpm} onChange={set('rate_limit_rpm')} placeholder="500" inputMode="numeric" />
        </Field>
        <Field label="Rate limit TPM">
          <Input value={form.rate_limit_tpm} onChange={set('rate_limit_tpm')} placeholder="200000" inputMode="numeric" />
        </Field>
      </div>
      <Field label="Metadata (JSON object, optional)">
        <textarea value={form.metadata} onChange={set('metadata')} placeholder='{"family": "gpt"}' rows={2} className={fieldClasses} />
      </Field>
      {formError ? <p className="text-xs text-danger">{formError}</p> : null}
      {success ? <p className="text-xs text-success">{success}</p> : null}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={pending}>
          {pending ? 'Saving…' : isEdit ? 'Save changes' : 'Create model'}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function ModelRow({ model }: { model: Model }) {
  const patch = usePatchModel();
  const remove = useDeleteModel();
  const [editing, setEditing] = React.useState(false);
  const [feedback, setFeedback] = React.useState<string | null>(null);

  const onToggle = () => {
    const next = model.status === 'active' ? 'disabled' : 'active';
    setFeedback(null);
    patch.mutate(
      { id: model.id, body: { status: next } },
      {
        onSuccess: () => setFeedback(next === 'active' ? 'enabled' : 'disabled'),
        onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'status change failed'),
      },
    );
  };

  const onDelete = () => {
    if (!window.confirm(`Delete model "${model.name}" from provider "${model.provider_name || model.provider_id}"?`)) return;
    setFeedback(null);
    remove.mutate(model.id, {
      onSuccess: () => setFeedback('deleted'),
      onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'delete failed'),
    });
  };

  return (
    <TableRow>
      <TableCell>
        <div className="space-y-0.5">
          <p className="text-sm font-medium">{model.display_name || model.name}</p>
          <p className="font-mono text-[11px] text-muted-foreground">{model.name}</p>
          {(model.aliases ?? []).length > 0 ? (
            <p className="text-[11px] text-muted-foreground">aliases: {(model.aliases ?? []).join(', ')}</p>
          ) : null}
          {feedback ? <p className="text-[11px] text-info">{feedback}</p> : null}
          {editing ? (
            <div className="rounded-md border border-border p-3">
              <ModelForm initial={formFromModel(model)} isEdit modelId={model.id} onClose={() => setEditing(false)} />
            </div>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-xs">{model.provider_name || model.provider_id}</TableCell>
      <TableCell>
        <Badge tone={STATUS_TONE[model.status] ?? 'neutral'} dot>
          {model.status}
        </Badge>
      </TableCell>
      <TableCell className="text-right text-xs tabular-nums">
        {model.context_window > 0 ? formatCompact(model.context_window) : '—'}
      </TableCell>
      <TableCell className="text-right text-xs tabular-nums">
        {model.max_output_tokens > 0 ? formatCompact(model.max_output_tokens) : '—'}
      </TableCell>
      <TableCell className="text-right text-xs tabular-nums">{formatCurrency(model.input_cost_per_million)}</TableCell>
      <TableCell className="text-right text-xs tabular-nums">{formatCurrency(model.output_cost_per_million)}</TableCell>
      <TableCell>
        <div className="flex max-w-xs flex-wrap gap-1">
          {(model.capabilities ?? []).map((capability) => (
            <Badge key={capability} tone="outline">
              {capability}
            </Badge>
          ))}
        </div>
      </TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          <Button variant="outline" size="sm" onClick={() => setEditing((value) => !value)}>
            {editing ? 'Close' : 'Edit'}
          </Button>
          <Button variant="outline" size="sm" onClick={onToggle} disabled={patch.isPending}>
            {model.status === 'active' ? 'Disable' : 'Enable'}
          </Button>
          <Button variant="ghost" size="sm" onClick={onDelete} disabled={remove.isPending}>
            <Trash2 />
            Delete
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

export function ModelsView() {
  const { data: models, isPending, isError, error, refetch, isFetching } = useModels();
  const { data: providers } = useProviders();
  const [providerFilter, setProviderFilter] = React.useState('');
  const [search, setSearch] = React.useState('');
  const [showCreate, setShowCreate] = React.useState(false);

  const providers_ = React.useMemo(() => {
    const names = new Set<string>();
    for (const model of models ?? []) {
      names.add(model.provider_name || model.provider_id);
    }
    return Array.from(names).sort();
  }, [models]);

  const filtered = React.useMemo(() => {
    const needle = search.trim().toLowerCase();
    return (models ?? []).filter((model) => {
      if (providerFilter && (model.provider_name || model.provider_id) !== providerFilter) return false;
      if (!needle) return true;
      return (
        model.name.toLowerCase().includes(needle) ||
        (model.display_name ?? '').toLowerCase().includes(needle) ||
        (model.aliases ?? []).some((alias) => alias.toLowerCase().includes(needle))
      );
    });
  }, [models, providerFilter, search]);

  const defaultProviderId = providers && providers.length > 0 && providers[0] ? providers[0].id : '';

  return (
    <>
      <PageHeader
        title="Models"
        description="The registry is one row per provider–model pair, not one row per model name. Two providers serving the same model are separate entries because they have their own base URL, credential and observed latency — which is what keeps a fallback chain honest."
        actions={
          <>
            <Button size="sm" onClick={() => setShowCreate((value) => !value)}>
              <Plus />
              {showCreate ? 'Hide form' : 'Add model'}
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
              <Boxes className="size-4" />
              Add model
            </CardTitle>
            <CardDescription>Name and provider are immutable after creation; everything else can be edited.</CardDescription>
          </CardHeader>
          <CardContent>
            <ModelForm initial={emptyModelForm(defaultProviderId)} isEdit={false} onClose={() => setShowCreate(false)} />
          </CardContent>
        </Card>
      ) : null}

      <Card className="mb-4">
        <CardContent className="flex flex-wrap items-end gap-3 pt-5">
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Provider
            <Select value={providerFilter} onChange={(event) => setProviderFilter(event.target.value)} className="w-52">
              <option value="">All providers</option>
              {providers_.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </Select>
          </label>
          <label className="flex flex-1 flex-col gap-1 text-xs text-muted-foreground">
            Search
            <div className="flex items-center gap-2">
              <Search className="size-4 text-muted-foreground" />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="model name, display name or alias"
              />
            </div>
          </label>
          <p className="text-xs text-muted-foreground">
            {formatNumber(filtered.length)} of {formatNumber((models ?? []).length)} models
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={8} columns={7} />
          ) : filtered.length === 0 ? (
            <EmptyState
              title="No models match"
              description="The registry is populated from the config's bootstrap catalogue or through the admin API. Clear the filters or add a model."
              className="m-5"
              action={
                <span className="mt-2 inline-flex items-center gap-1 text-xs text-muted-foreground">
                  <Boxes className="size-3.5" />
                  {formatNumber((models ?? []).length)} registered in total
                </span>
              }
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Model</TableHead>
                  <TableHead>Provider</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Context</TableHead>
                  <TableHead className="text-right">Max output</TableHead>
                  <TableHead className="text-right">Input $/M</TableHead>
                  <TableHead className="text-right">Output $/M</TableHead>
                  <TableHead>Capabilities</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((model) => (
                  <ModelRow key={model.id} model={model} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
