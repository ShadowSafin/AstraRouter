'use client';

import { ChevronDown, Download, KeyRound, PlugZap, Plus, RefreshCw, ServerCog, Trash2 } from 'lucide-react';
import * as React from 'react';

import { PageHeader } from '@/components/page-header';
import { Badge, HealthBadge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select, fieldClasses } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { cn } from '@/lib/utils';
import {
  useCreateProvider,
  useDeleteCredential,
  useDeleteProvider,
  useKillProvider,
  usePatchProvider,
  useProbeProvider,
  useProviders,
  useSetCredential,
  useSyncModels,
  useTestProvider,
  useUpdateProvider,
} from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import { formatDurationMs, formatPercent, formatRelative } from '@/lib/format';
import type { ProviderSummary, ProviderTestResult } from '@/lib/types';

const KIND_OPTIONS = ['openai', 'anthropic', 'ollama', 'vllm', 'openai_compatible'];
const STATUS_OPTIONS = ['active', 'degraded', 'disabled', 'pending'];
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

function parseJsonObject(value: string): { ok: boolean; parsed?: Record<string, string>; error?: string } {
  const trimmed = value.trim();
  if (!trimmed) return { ok: true, parsed: undefined };
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      return { ok: false, error: 'must be a JSON object' };
    }
    return { ok: true, parsed: parsed as Record<string, string> };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : 'invalid JSON' };
  }
}

interface ProviderFormState {
  name: string;
  kind: string;
  base_url: string;
  api_key_env: string;
  auth_style: string;
  header_name: string;
  headers: string;
  organization: string;
  project: string;
  capabilities: string;
  status: string;
  weight: string;
  priority: string;
  timeout_ms: string;
  max_concurrency: string;
  region: string;
  notes: string;
  environment: string;
  labels: string;
  credential_secret: string;
  credential_name: string;
}

function emptyForm(): ProviderFormState {
  return {
    name: '',
    kind: 'openai_compatible',
    base_url: '',
    api_key_env: '',
    auth_style: '',
    header_name: '',
    headers: '',
    organization: '',
    project: '',
    capabilities: '',
    status: 'active',
    weight: '1',
    priority: '100',
    timeout_ms: '',
    max_concurrency: '',
    region: '',
    notes: '',
    environment: '',
    labels: '',
    credential_secret: '',
    credential_name: '',
  };
}

function formFromProvider(provider: ProviderSummary): ProviderFormState {
  const base = emptyForm();
  return {
    ...base,
    name: provider.name,
    kind: provider.kind || 'openai_compatible',
    base_url: provider.base_url,
    capabilities: (provider.capabilities ?? []).join(', '),
    status: provider.status,
    weight: String(provider.weight || 1),
    priority: String(provider.priority || 0),
    region: provider.region || '',
    notes: provider.notes || '',
    environment: provider.environment || '',
    labels: provider.labels && Object.keys(provider.labels).length > 0 ? JSON.stringify(provider.labels) : '',
    credential_secret: '',
    credential_name: '',
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

function ProviderForm({
  initial,
  isEdit,
  onClose,
}: {
  initial: ProviderFormState;
  isEdit: boolean;
  onClose: () => void;
}) {
  const createProvider = useCreateProvider();
  const updateProvider = useUpdateProvider();
  const setCredential = useSetCredential();
  const [form, setForm] = React.useState<ProviderFormState>(initial);
  const [formError, setFormError] = React.useState<string | null>(null);
  const [success, setSuccess] = React.useState<string | null>(null);

  const set = (key: keyof ProviderFormState) => (event: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
    setForm((current) => ({ ...current, [key]: event.target.value }));
  };

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setFormError(null);
    setSuccess(null);

    if (!form.name.trim()) {
      setFormError('name is required');
      return;
    }
    if (!form.base_url.trim()) {
      setFormError('base_url is required');
      return;
    }
    const headers = parseJsonObject(form.headers);
    if (!headers.ok) {
      setFormError(`headers: ${headers.error}`);
      return;
    }
    const labels = parseJsonObject(form.labels);
    if (!labels.ok) {
      setFormError(`labels: ${labels.error}`);
      return;
    }
    const weight = parseOptionalNumber(form.weight);
    const priority = parseOptionalNumber(form.priority);
    const timeoutMs = parseOptionalNumber(form.timeout_ms);
    const maxConcurrency = parseOptionalNumber(form.max_concurrency);
    if (form.weight.trim() && weight === undefined) {
      setFormError('weight must be a number');
      return;
    }
    if (form.priority.trim() && priority === undefined) {
      setFormError('priority must be a number');
      return;
    }
    if (form.timeout_ms.trim() && timeoutMs === undefined) {
      setFormError('timeout_ms must be a number');
      return;
    }
    if (form.max_concurrency.trim() && maxConcurrency === undefined) {
      setFormError('max_concurrency must be a number');
      return;
    }

    const body: Record<string, unknown> = {
      name: form.name.trim(),
      kind: form.kind,
      base_url: form.base_url.trim(),
      status: form.status,
    };
    if (form.api_key_env.trim()) body.api_key_env = form.api_key_env.trim();
    if (form.auth_style.trim()) body.auth_style = form.auth_style.trim();
    if (form.header_name.trim()) body.header_name = form.header_name.trim();
    if (headers.parsed) body.headers = headers.parsed;
    if (form.organization.trim()) body.organization = form.organization.trim();
    if (form.project.trim()) body.project = form.project.trim();
    const capabilities = parseCsv(form.capabilities);
    if (capabilities.length > 0) body.capabilities = capabilities;
    if (weight !== undefined) body.weight = weight;
    if (priority !== undefined) body.priority = priority;
    if (timeoutMs !== undefined) body.timeout_ms = timeoutMs;
    if (maxConcurrency !== undefined) body.max_concurrency = maxConcurrency;
    if (form.region.trim()) body.region = form.region.trim();
    if (form.notes.trim()) body.notes = form.notes.trim();
    if (form.environment.trim()) body.environment = form.environment.trim();
    if (labels.parsed) body.labels = labels.parsed;

    const afterSave = (providerId: string) => {
      // A credential secret is write-only: it is set through the dedicated
      // endpoint after the provider exists and is never echoed back.
      if (form.credential_secret) {
        setCredential.mutate(
          {
            providerId,
            secret: form.credential_secret,
            name: form.credential_name.trim() || undefined,
          },
          {
            onSuccess: () => setSuccess('saved, credential stored'),
            onError: (mutationError) =>
              setFormError(
                mutationError instanceof ApiError
                  ? `saved, but the credential was rejected: ${mutationError.message}`
                  : 'saved, but the credential could not be stored',
              ),
          },
        );
      } else {
        setSuccess('saved');
      }
    };

    if (isEdit) {
      updateProvider.mutate(
        { id: form.name.trim(), body },
        {
          onSuccess: (provider) => {
            afterSave(provider.id || provider.name);
            window.setTimeout(onClose, 1200);
          },
          onError: (mutationError) =>
            setFormError(mutationError instanceof ApiError ? mutationError.message : 'the provider could not be updated'),
        },
      );
    } else {
      createProvider.mutate(body as unknown as Parameters<typeof createProvider.mutate>[0], {
        onSuccess: (provider) => {
          afterSave(provider.id || provider.name);
          setForm(emptyForm());
        },
        onError: (mutationError) =>
          setFormError(mutationError instanceof ApiError ? mutationError.message : 'the provider could not be created'),
      });
    }
  };

  const pending = createProvider.isPending || updateProvider.isPending || setCredential.isPending;

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="Name (immutable after creation)">
          <Input value={form.name} onChange={set('name')} placeholder="openai-primary" disabled={isEdit} />
        </Field>
        <Field label="Kind">
          <Select value={form.kind} onChange={set('kind')}>
            {KIND_OPTIONS.map((kind) => (
              <option key={kind} value={kind}>
                {kind}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Base URL">
          <Input value={form.base_url} onChange={set('base_url')} placeholder="https://api.openai.com/v1" />
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
        <Field label="Region">
          <Input value={form.region} onChange={set('region')} placeholder="us-east-1" />
        </Field>
        <Field label="API key env">
          <Input value={form.api_key_env} onChange={set('api_key_env')} placeholder="OPENAI_API_KEY" />
        </Field>
        <Field label="Auth style">
          <Input value={form.auth_style} onChange={set('auth_style')} placeholder="bearer" />
        </Field>
        <Field label="Header name">
          <Input value={form.header_name} onChange={set('header_name')} placeholder="Authorization" />
        </Field>
        <Field label="Organization">
          <Input value={form.organization} onChange={set('organization')} placeholder="org-..." />
        </Field>
        <Field label="Project">
          <Input value={form.project} onChange={set('project')} placeholder="proj-..." />
        </Field>
        <Field label="Capabilities (comma-separated)">
          <Input value={form.capabilities} onChange={set('capabilities')} placeholder="chat, embeddings" />
        </Field>
        <Field label="Weight">
          <Input value={form.weight} onChange={set('weight')} placeholder="1" inputMode="numeric" />
        </Field>
        <Field label="Priority">
          <Input value={form.priority} onChange={set('priority')} placeholder="100" inputMode="numeric" />
        </Field>
        <Field label="Timeout ms">
          <Input value={form.timeout_ms} onChange={set('timeout_ms')} placeholder="30000" inputMode="numeric" />
        </Field>
        <Field label="Max concurrency">
          <Input value={form.max_concurrency} onChange={set('max_concurrency')} placeholder="32" inputMode="numeric" />
        </Field>
        <Field label="Credential name (optional)">
          <Input value={form.credential_name} onChange={set('credential_name')} placeholder="primary" />
        </Field>
        <Field label="Credential secret (write-only, never displayed)">
          <Input
            type="password"
            autoComplete="new-password"
            value={form.credential_secret}
            onChange={set('credential_secret')}
            placeholder={isEdit ? 'leave blank to keep the current secret' : 'sk-...'}
          />
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Headers (JSON object, optional)">
          <textarea
            value={form.headers}
            onChange={set('headers')}
            placeholder='{"X-Custom": "value"}'
            rows={2}
            className={fieldClasses}
          />
        </Field>
        <Field label="Labels (JSON object, optional)">
          <textarea
            value={form.labels}
            onChange={set('labels')}
            placeholder='{"team": "ml"}'
            rows={2}
            className={fieldClasses}
          />
        </Field>
      </div>
      <Field label="Notes (optional)">
        <textarea
          value={form.notes}
          onChange={set('notes')}
          placeholder="why this provider exists, who owns it"
          rows={2}
          className={fieldClasses}
        />
      </Field>
      {formError ? <p className="text-xs text-danger">{formError}</p> : null}
      {success ? <p className="text-xs text-success">{success}</p> : null}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={pending}>
          {pending ? 'Saving…' : isEdit ? 'Save changes' : 'Create provider'}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function TestResults({ results }: { results: ProviderTestResult[] }) {
  if (results.length === 0) return <p className="text-[11px] text-muted-foreground">no check results</p>;
  return (
    <ul className="space-y-1">
      {results.map((result, index) => (
        <li key={`${result.kind}-${index}`} className="flex flex-wrap items-center gap-2 text-[11px]">
          <Badge tone={result.success ? 'success' : 'danger'}>
            {result.kind}: {result.success ? 'pass' : 'fail'}
          </Badge>
          {result.latency_ms !== undefined ? (
            <span className="tabular-nums text-muted-foreground">{formatDurationMs(result.latency_ms)}</span>
          ) : null}
          {result.message ? <span className="text-muted-foreground">{result.message}</span> : null}
        </li>
      ))}
    </ul>
  );
}

function ProviderRow({ provider }: { provider: ProviderSummary }) {
  const probe = useProbeProvider();
  const kill = useKillProvider();
  const patch = usePatchProvider();
  const remove = useDeleteProvider();
  const test = useTestProvider();
  const sync = useSyncModels();
  const setCredential = useSetCredential();
  const deleteCredential = useDeleteCredential();
  const [feedback, setFeedback] = React.useState<string | null>(null);
  const [editing, setEditing] = React.useState(false);
  const [showCred, setShowCred] = React.useState(false);
  const [credSecret, setCredSecret] = React.useState('');
  const [credName, setCredName] = React.useState('');
  const [credSync, setCredSync] = React.useState(true);
  const [testResults, setTestResults] = React.useState<ProviderTestResult[] | null>(null);
  const [open, setOpen] = React.useState(false);

  const onProbe = () => {
    setFeedback(null);
    probe.mutate(provider.id, {
      onSuccess: (health) => setFeedback(`probe: ${health.state} in ${formatDurationMs(health.latency_ms)}`),
      onError: (error) =>
        setFeedback(error instanceof ApiError ? error.message : 'the probe could not be completed'),
    });
  };

  const onKill = (doKill: boolean) => {
    kill.mutate(
      { id: provider.name || provider.id, kill: doKill, reason: doKill ? 'operator kill switch from dashboard' : undefined },
      {
        onSuccess: (r) => setFeedback(r.killed ? 'killed — removed from rotation' : 'revived — back in rotation'),
        onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'override failed'),
      },
    );
  };

  const onToggleEnabled = () => {
    setFeedback(null);
    const next = provider.status === 'active' ? 'disabled' : 'active';
    patch.mutate(
      { id: provider.id, body: { status: next } },
      {
        onSuccess: () => setFeedback(next === 'active' ? 'enabled — back in rotation' : 'disabled — out of rotation'),
        onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'status change failed'),
      },
    );
  };

  const onTest = () => {
    setFeedback(null);
    setTestResults(null);
    test.mutate(
      { providerId: provider.id, checks: ['connectivity', 'models', 'sample'] },
      {
        onSuccess: (response) => {
          setTestResults(response.results ?? []);
          setFeedback(response.success ? 'all checks passed' : 'some checks failed — see results');
        },
        onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'the test could not be completed'),
      },
    );
  };

  const onDelete = () => {
    if (!window.confirm(`Delete provider "${provider.name}"? Its models will also be removed.`)) return;
    setFeedback(null);
    remove.mutate(provider.id, {
      onSuccess: (r) => setFeedback(`deleted, ${r.models_removed} model(s) removed`),
      onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'delete failed'),
    });
  };

  const onSync = () => {
    setFeedback(null);
    sync.mutate(provider.id, {
      onSuccess: (r) => {
        const created = r.created ?? [];
        const skipped = r.skipped ?? [];
        setFeedback(
          `synced ${r.total} remote model(s): ${created.length} added, ${skipped.length} already registered`,
        );
      },
      onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'model sync failed'),
    });
  };

  const onSaveCredential = () => {
    if (!credSecret) {
      setFeedback('enter a secret to store');
      return;
    }
    setFeedback(null);
    setCredential.mutate(
      { providerId: provider.id, secret: credSecret, name: credName.trim() || undefined, syncModels: credSync },
      {
        onSuccess: (response) => {
          if (response.sync) {
            const created = response.sync.created ?? [];
            setFeedback(`credential stored, ${created.length} model(s) discovered`);
          } else if (response.sync_error) {
            setFeedback(`credential stored, model sync failed: ${response.sync_error}`);
          } else {
            setFeedback('credential stored');
          }
          setCredSecret('');
          setCredName('');
          setShowCred(false);
        },
        onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'credential store failed'),
      },
    );
  };

  const onClearCredential = () => {
    if (!window.confirm(`Remove the stored credential for "${provider.name}"?`)) return;
    deleteCredential.mutate(provider.id, {
      onSuccess: () => setFeedback('credential removed'),
      onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'credential removal failed'),
    });
  };

  return (
    <>
    <TableRow>
      <TableCell>
        <div className="space-y-0.5">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{provider.name}</span>
            <Badge tone="outline">{provider.kind}</Badge>
            {provider.has_credential ? (
              <Badge tone="success">
                <KeyRound className="mr-1 size-3" />
                credential
              </Badge>
            ) : (
              <Badge tone="neutral">no credential</Badge>
            )}
          </div>
          <p className="font-mono text-[11px] text-muted-foreground">{provider.base_url}</p>
          {provider.environment ? (
            <p className="text-[11px] text-muted-foreground">env: {provider.environment}</p>
          ) : null}
          {provider.notes ? <p className="max-w-xs text-[11px] text-muted-foreground">{provider.notes}</p> : null}
        </div>
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-1.5">
          <HealthBadge state={provider.health?.state} />
          <Badge tone={provider.status === 'active' ? 'success' : 'neutral'} dot>
            {provider.status}
          </Badge>
        </div>
      </TableCell>
      <TableCell className="text-xs tabular-nums">{provider.model_count}</TableCell>
      <TableCell className="text-xs tabular-nums">{provider.priority || '—'}</TableCell>
      <TableCell className="text-xs tabular-nums">{provider.weight || 1}</TableCell>
      <TableCell className="text-xs">
        {provider.health ? (
          <div className="space-y-0.5">
            <p className="tabular-nums">{formatDurationMs(provider.health.latency_ms)}</p>
            <p className="text-muted-foreground">
              {formatPercent(provider.health.error_rate)} errors · {formatRelative(provider.health.checked_at)}
            </p>
          </div>
        ) : (
          <span className="text-muted-foreground">no assessment yet</span>
        )}
      </TableCell>
      <TableCell className="text-right">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setOpen((value) => !value)}
          aria-expanded={open}
          aria-label={open ? `Collapse ${provider.name}` : `Manage ${provider.name}`}
          title={open ? 'Collapse' : 'Manage'}
        >
          <ChevronDown className={cn('size-4 transition-transform', open && 'rotate-180')} />
        </Button>
      </TableCell>
    </TableRow>
    {open ? (
      <TableRow>
        <TableCell colSpan={7} className="bg-white/[0.015] p-0">
          <div className="space-y-4 px-4 py-4 sm:px-5">
            <div className="flex flex-wrap items-center gap-2">
              {provider.adapter_ready ? (
                <Badge tone="info">adapter ready</Badge>
              ) : (
                <Badge tone="danger">no adapter</Badge>
              )}
              {feedback ? <p className="text-xs text-info">{feedback}</p> : null}
            </div>
            <div className="grid gap-4 md:grid-cols-3">
              <div className="space-y-2">
                <p className="text-[11px] font-semibold uppercase tracking-[0.06em] text-muted-foreground">
                  Assess
                </p>
                <div className="flex flex-wrap gap-1.5">
                  <Button variant="outline" size="sm" onClick={onProbe} disabled={probe.isPending}>
                    <PlugZap className={probe.isPending ? 'animate-pulse' : undefined} />
                    {probe.isPending ? 'Probing…' : 'Probe'}
                  </Button>
                  <Button variant="outline" size="sm" onClick={onTest} disabled={test.isPending}>
                    {test.isPending ? 'Testing…' : 'Test'}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={onSync}
                    disabled={sync.isPending}
                    title="Discover remote models and add missing ones to the registry"
                  >
                    <Download className={sync.isPending ? 'animate-pulse' : undefined} />
                    {sync.isPending ? 'Syncing…' : 'Sync models'}
                  </Button>
                </div>
              </div>
              <div className="space-y-2">
                <p className="text-[11px] font-semibold uppercase tracking-[0.06em] text-muted-foreground">
                  Configure
                </p>
                <div className="flex flex-wrap gap-1.5">
                  <Button variant="outline" size="sm" onClick={() => setEditing((value) => !value)}>
                    {editing ? 'Close' : 'Edit'}
                  </Button>
                  <Button variant="outline" size="sm" onClick={onToggleEnabled} disabled={patch.isPending}>
                    {provider.status === 'active' ? 'Disable' : 'Enable'}
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => setShowCred((value) => !value)}>
                    {showCred ? 'Hide secret' : 'Set secret'}
                  </Button>
                  {provider.has_credential ? (
                    <Button variant="ghost" size="sm" onClick={onClearCredential} disabled={deleteCredential.isPending}>
                      Clear secret
                    </Button>
                  ) : null}
                </div>
              </div>
              <div className="space-y-2">
                <p className="text-[11px] font-semibold uppercase tracking-[0.06em] text-muted-foreground">
                  Danger
                </p>
                <div className="flex flex-wrap gap-1.5">
                  <Button variant="destructive" size="sm" onClick={() => onKill(true)} disabled={kill.isPending} title="Kill switch: remove from rotation immediately">
                    Kill
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => onKill(false)} disabled={kill.isPending} title="Revive a killed provider">
                    Revive
                  </Button>
                  <Button variant="ghost" size="sm" onClick={onDelete} disabled={remove.isPending} title="Delete the provider and its models">
                    <Trash2 />
                    Delete
                  </Button>
                </div>
              </div>
            </div>
            {testResults ? (
              <div className="rounded-lg border border-border p-3">
                <TestResults results={testResults} />
              </div>
            ) : null}
            {editing ? (
              <div className="rounded-lg border border-border p-4">
                <ProviderForm initial={formFromProvider(provider)} isEdit onClose={() => setEditing(false)} />
              </div>
            ) : null}
            {showCred ? (
              <div className="flex max-w-md flex-col gap-2 rounded-lg border border-border p-4">
                <p className="text-xs text-muted-foreground">
                  Secrets are write-only and never displayed. Setting a new secret replaces the current one.
                </p>
                <Input
                  value={credName}
                  onChange={(event) => setCredName(event.target.value)}
                  placeholder="credential name (optional)"
                />
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={credSecret}
                  onChange={(event) => setCredSecret(event.target.value)}
                  placeholder="new secret"
                />
                <label className="flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={credSync}
                    onChange={(event) => setCredSync(event.target.checked)}
                    className="size-4 accent-primary"
                  />
                  Discover remote models into the registry after storing
                </label>
                <div className="flex gap-2">
                  <Button size="sm" onClick={onSaveCredential} disabled={setCredential.isPending}>
                    {setCredential.isPending ? 'Storing…' : 'Store'}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setShowCred(false)}>
                    Cancel
                  </Button>
                </div>
              </div>
            ) : null}
          </div>
        </TableCell>
      </TableRow>
    ) : null}
    </>
  );
}

export function ProvidersView() {
  const { data: providers, isPending, isError, error, refetch, isFetching } = useProviders();
  const [showCreate, setShowCreate] = React.useState(false);

  return (
    <>
      <PageHeader
        title="Providers"
        description="Upstream inference endpoints and their live health. 'Adapter ready' means a working client could be constructed for the provider — a configured provider with a missing credential shows as configured but not usable."
        actions={
          <>
            <Button size="sm" onClick={() => setShowCreate((value) => !value)}>
              <Plus />
              {showCreate ? 'Hide form' : 'Add provider'}
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
              <ServerCog className="size-4" />
              Add provider
            </CardTitle>
            <CardDescription>
              The secret is write-only: it is stored through the credential endpoint and never rendered back.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ProviderForm initial={emptyForm()} isEdit={false} onClose={() => setShowCreate(false)} />
          </CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ServerCog className="size-4" />
            Configured providers
          </CardTitle>
          <CardDescription>
            Priority orders candidates for the priority strategy. Weight biases the weighted strategy.
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={5} columns={6} />
          ) : (providers ?? []).length === 0 ? (
            <EmptyState
              title="No providers configured"
              description="Add one above, declare providers in the gateway config's `providers` section, or apply the bootstrap catalogue on startup. Nothing can be routed until at least one provider exists."
              className="m-5"
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Provider</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead className="text-right">Models</TableHead>
                  <TableHead className="text-right">Priority</TableHead>
                  <TableHead className="text-right">Weight</TableHead>
                  <TableHead>Health</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(providers ?? []).map((provider) => (
                  <ProviderRow key={provider.id} provider={provider} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
