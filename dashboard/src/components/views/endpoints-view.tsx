'use client';

import * as React from 'react';
import { Check, Copy, Network, Trash2 } from 'lucide-react';
import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input, Select } from '@/components/ui/input';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useDeleteEndpoint, useEndpoints, useModels, useProviders, useUpsertEndpoint } from '@/hooks/use-admin';
import { ApiError } from '@/lib/api';
import type { Endpoint } from '@/lib/types';

const STRATEGY_OPTIONS = ['', 'priority', 'weighted', 'lowest_cost', 'lowest_latency', 'highest_quality'];

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

type ModelScopeMode = 'all' | 'provider' | 'single' | 'custom';

interface EndpointFormState {
  slug: string;
  name: string;
  description: string;
  enabled: boolean;
  strategy: string;
  model_mode: ModelScopeMode;
  scope_provider: string;
  scope_model: string;
  custom_models: string;
  custom_providers: string;
  max_cost_usd: string;
  latency_target_ms: string;
  use_cache: string;
  block_fallback: boolean;
}

function emptyForm(): EndpointFormState {
  return {
    slug: '',
    name: '',
    description: '',
    enabled: true,
    strategy: '',
    model_mode: 'all',
    scope_provider: '',
    scope_model: '',
    custom_models: '',
    custom_providers: '',
    max_cost_usd: '',
    latency_target_ms: '',
    use_cache: '',
    block_fallback: false,
  };
}

interface CatalogueLookups {
  providerIdByName: (name: string) => string;
  modelByName: (name: string) => { id: string; provider_id: string } | undefined;
}

function formFromEndpoint(e: Endpoint, lookups: CatalogueLookups): EndpointFormState {
  const base = emptyForm();
  const override = e.routing_override;
  const form: EndpointFormState = {
    ...base,
    slug: e.slug,
    name: e.name || e.slug,
    description: e.description || '',
    enabled: e.enabled,
    strategy: override?.strategy || '',
    max_cost_usd: override?.max_cost_usd !== undefined ? String(override.max_cost_usd) : '',
    latency_target_ms: override?.latency_target_ms !== undefined ? String(override.latency_target_ms) : '',
    use_cache: override?.use_cache === undefined ? '' : String(override.use_cache),
    block_fallback: override?.block_fallback || false,
  };

  const forced = override?.force_model || '';
  const preferredModels = override?.preferred_models ?? [];
  const preferredProviders = override?.preferred_providers ?? [];
  if (forced) {
    const found = lookups.modelByName(forced);
    if (found) {
      form.model_mode = 'single';
      form.scope_provider = found.provider_id;
      form.scope_model = found.id;
      return form;
    }
  }
  if (preferredModels.length === 0 && preferredProviders.length === 1 && !forced) {
    const onlyProvider = preferredProviders[0] ?? '';
    const providerId = onlyProvider ? lookups.providerIdByName(onlyProvider) : '';
    if (providerId) {
      form.model_mode = 'provider';
      form.scope_provider = providerId;
      return form;
    }
  }
  if (preferredModels.length === 0 && preferredProviders.length === 0 && !forced) {
    form.model_mode = 'all';
    return form;
  }
  // Anything hand-built (multi-model lists, multi-provider lists, or a forced
  // model the registry no longer has) keeps its exact values in custom mode
  // so editing never silently drops constraints.
  form.model_mode = 'custom';
  if (forced) form.custom_models = forced;
  else form.custom_models = preferredModels.join(', ');
  form.custom_providers = preferredProviders.join(', ');
  return form;
}

function useGatewayUrls(): { local: string; lan: string | null } {
  // The build-time public URL is the address a browser can reach (compose
  // inlines NEXT_PUBLIC_COREROUTER_API_URL). The server-side gateway address
  // is only a fallback: inside Docker it is a service name no browser can
  // resolve.
  const baked = process.env.NEXT_PUBLIC_COREROUTER_API_URL;
  const [urls, setUrls] = React.useState<{ local: string; lan: string | null }>({
    local: baked && baked.length > 0 ? baked : 'http://GATEWAY:PORT',
    lan: null,
  });
  React.useEffect(() => {
    let cancelled = false;
    fetch('/api/client-config')
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled || !data) return;
        setUrls((prev) => ({
          local:
            baked && baked.length > 0
              ? baked
              : typeof data.gateway_url === 'string' && data.gateway_url
                ? data.gateway_url
                : prev.local,
          lan: typeof data.lan_url === 'string' && data.lan_url ? data.lan_url : null,
        }));
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [baked]);
  return urls;
}

function curlSnippet(gatewayUrl: string, slug: string): string {
  return [
    `curl -s ${gatewayUrl}/v1/chat/completions \\`,
    '  -H "Authorization: Bearer $CR_KEY" \\',
    '  -H "Content-Type: application/json" \\',
    `  -H "X-CoreRouter-Endpoint: ${slug}" \\`,
    `  -d '{"model": "any-registered-model", "messages": [{"role": "user", "content": "hi"}]}'`,
  ].join('\n');
}

function CopyButton({ text, label, caption }: { text: string; label: string; caption?: string }) {
  const [copied, setCopied] = React.useState(false);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };
  return (
    <Button variant="outline" size="sm" onClick={() => void onCopy()} title={label}>
      {copied ? <Check /> : <Copy />}
      {copied ? 'Copied' : (caption ?? 'Copy curl')}
    </Button>
  );
}

function describeOverride(e: Endpoint): string {
  const parts: string[] = [];
  const override = e.routing_override;
  const models = override?.preferred_models ?? [];
  const providers = override?.preferred_providers ?? [];

  // The model scope is the first thing an operator needs to read: it decides
  // what can serve this endpoint at all.
  if (override?.force_model) {
    parts.push(`only ${override.force_model}`);
  } else if (models.length > 0) {
    parts.push(`models: ${models.join(', ')}`);
  } else if (providers.length > 0) {
    parts.push(`only provider ${providers.join(', ')}`);
  } else {
    parts.push('all models');
  }
  if (override?.strategy) parts.push(override.strategy);
  if (override?.max_cost_usd !== undefined) parts.push(`max $${override.max_cost_usd}`);
  if (override?.latency_target_ms !== undefined) parts.push(`${override.latency_target_ms}ms`);
  if (override?.block_fallback) parts.push('no fallback');
  return parts.join(' · ');
}

export function EndpointsView() {
  const { local: gatewayUrl, lan: lanUrl } = useGatewayUrls();
  const { data, isPending, isError, error, refetch } = useEndpoints();
  const { data: providers } = useProviders();
  const { data: models } = useModels();
  const upsert = useUpsertEndpoint();
  const remove = useDeleteEndpoint();
  const [form, setForm] = React.useState<EndpointFormState>(emptyForm);
  const [editingId, setEditingId] = React.useState<string | null>(null);
  const [feedback, setFeedback] = React.useState<string | null>(null);
  const [formError, setFormError] = React.useState<string | null>(null);

  const endpoints = data ?? [];
  const providerList = providers ?? [];
  const modelList = models ?? [];

  const lookups: CatalogueLookups = React.useMemo(() => {
    const providersByName = new Map(providerList.map((p) => [p.name.toLowerCase(), p.id]));
    const modelsByName = new Map(
      modelList.map((m) => [m.name.toLowerCase(), { id: m.id, provider_id: m.provider_id }]),
    );
    return {
      providerIdByName: (name: string) => providersByName.get(name.toLowerCase()) ?? '',
      modelByName: (name: string) => modelsByName.get(name.toLowerCase()),
    };
  }, [providerList, modelList]);

  const providerById = React.useMemo(
    () => new Map(providerList.map((p) => [p.id, p])),
    [providerList],
  );
  const modelsForProvider = React.useMemo(
    () => modelList.filter((m) => !form.scope_provider || m.provider_id === form.scope_provider),
    [modelList, form.scope_provider],
  );

  const set = (patch: Partial<EndpointFormState>) => setForm((prev) => ({ ...prev, ...patch }));

  const onEdit = (e: Endpoint) => {
    setForm(formFromEndpoint(e, lookups));
    setEditingId(e.id);
    setFeedback(null);
    setFormError(null);
  };

  const onCancel = () => {
    setForm(emptyForm());
    setEditingId(null);
    setFormError(null);
  };

  const onSave = () => {
    setFeedback(null);
    const slug = form.slug.trim();
    if (!slug) {
      setFormError('a slug is required, e.g. mobile-chat');
      return;
    }
    if (form.max_cost_usd.trim() && parseOptionalNumber(form.max_cost_usd) === undefined) {
      setFormError('max cost must be a number');
      return;
    }
    if (form.latency_target_ms.trim() && parseOptionalNumber(form.latency_target_ms) === undefined) {
      setFormError('latency target must be a number of milliseconds');
      return;
    }
    const override: Record<string, unknown> = {};
    if (form.strategy) override.strategy = form.strategy;
    if (form.model_mode === 'single') {
      const model = modelList.find((m) => m.id === form.scope_model);
      const provider = model ? providerById.get(model.provider_id) : undefined;
      if (!model) {
        setFormError('pick a model for single-model scope');
        return;
      }
      override.force_model = model.name;
      if (provider) override.preferred_providers = [provider.name];
    } else if (form.model_mode === 'provider') {
      const provider = providerById.get(form.scope_provider);
      if (!provider) {
        setFormError('pick a provider for single-provider scope');
        return;
      }
      override.preferred_providers = [provider.name];
    } else if (form.model_mode === 'custom') {
      const models = parseCsv(form.custom_models);
      if (models.length > 0) override.preferred_models = models;
      const providers = parseCsv(form.custom_providers);
      if (providers.length > 0) override.preferred_providers = providers;
    }
    const maxCost = parseOptionalNumber(form.max_cost_usd);
    if (maxCost !== undefined) override.max_cost_usd = maxCost;
    const latency = parseOptionalNumber(form.latency_target_ms);
    if (latency !== undefined) override.latency_target_ms = latency;
    if (form.use_cache === 'true' || form.use_cache === 'false') override.use_cache = form.use_cache === 'true';
    if (form.block_fallback) override.block_fallback = true;

    upsert.mutate(
      {
        slug,
        name: form.name.trim() || slug,
        description: form.description.trim(),
        routing_override: override,
        enabled: form.enabled,
      },
      {
        onSuccess: (saved) => {
          setFeedback(`saved endpoint "${saved.slug}" — call it with X-CoreRouter-Endpoint: ${saved.slug}`);
          onCancel();
        },
        onError: (e) => setFormError(e instanceof ApiError ? e.message : 'save failed'),
      },
    );
  };

  const onDelete = (id: string, endpointSlug: string) => {
    if (!window.confirm(`Delete endpoint "${endpointSlug}"? Clients sending that header fall back to policy routing.`)) return;
    setFeedback(null);
    remove.mutate(id, {
      onSuccess: () => setFeedback(`deleted "${endpointSlug}"`),
      onError: (e) => setFeedback(e instanceof ApiError ? e.message : 'delete failed'),
    });
  };

  return (
    <>
      <PageHeader
        title="Endpoints"
        description="Named routing scopes for classes of traffic. An endpoint is not a separate URL: every client calls the same inference URL and selects a scope with the X-CoreRouter-Endpoint header."
        note={feedback ? <span className="text-info">{feedback}</span> : undefined}
      />
      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Network className="size-4" />
            How clients call an endpoint
          </CardTitle>
          <CardDescription>
            One inference URL for everything. The header picks the scope; without it, policy routing applies.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <p className="text-xs font-medium">This machine (localhost)</p>
            <p className="font-mono text-xs">
              POST <span className="font-semibold">{gatewayUrl}/v1/chat/completions</span>
            </p>
            <p className="font-mono text-xs">
              X-CoreRouter-Endpoint: <span className="font-semibold">{form.slug.trim() || 'your-slug'}</span>
            </p>
            <pre className="overflow-x-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-[11px]">
              {curlSnippet(gatewayUrl, form.slug.trim() || 'your-slug')}
            </pre>
            <CopyButton text={curlSnippet(gatewayUrl, form.slug.trim() || 'your-slug')} label="Copy the localhost curl example" />
          </div>
          {lanUrl ? (
            <div className="space-y-2">
              <p className="text-xs font-medium">Local network (any device on your LAN)</p>
              <p className="font-mono text-xs">
                POST <span className="font-semibold">{lanUrl}/v1/chat/completions</span>
              </p>
              <pre className="overflow-x-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-[11px]">
                {curlSnippet(lanUrl, form.slug.trim() || 'your-slug')}
              </pre>
              <CopyButton text={curlSnippet(lanUrl, form.slug.trim() || 'your-slug')} label="Copy the LAN curl example" />
            </div>
          ) : (
            <p className="text-[11px] text-muted-foreground">
              No LAN address configured, so only this machine&apos;s URL is shown. Set GATEWAY_LAN_URL (e.g.
              http://192.168.1.20:18080) and bind the gateway to your network to share endpoints with other devices.
            </p>
          )}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Endpoints</CardTitle>
          <CardDescription>Copy the curl snippet per row to hand a working call to app developers.</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isPending ? (
            <TableSkeleton rows={4} columns={4} />
          ) : endpoints.length === 0 ? (
            <EmptyState title="No endpoints" description="Create one below: pick a slug, set the routing override, then copy the curl." className="m-5" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Slug</TableHead>
                  <TableHead>Override</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {endpoints.map((e) => (
                  <TableRow key={e.id}>
                    <TableCell>
                      <p className="font-mono text-xs font-medium">{e.slug}</p>
                      {e.description ? <p className="max-w-xs text-[11px] text-muted-foreground">{e.description}</p> : null}
                    </TableCell>
                    <TableCell className="max-w-md text-xs text-muted-foreground">{describeOverride(e)}</TableCell>
                    <TableCell>
                      <Badge tone={e.enabled ? 'success' : 'neutral'} dot>{e.enabled ? 'enabled' : 'disabled'}</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex flex-wrap justify-end gap-1">
                        <CopyButton text={curlSnippet(gatewayUrl, e.slug)} label={`Copy localhost curl for ${e.slug}`} caption="Copy local" />
                        {lanUrl ? (
                          <CopyButton text={curlSnippet(lanUrl, e.slug)} label={`Copy LAN curl for ${e.slug}`} caption="Copy LAN" />
                        ) : null}
                        <Button variant="outline" size="sm" onClick={() => onEdit(e)}>
                          Edit
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => onDelete(e.id, e.slug)} disabled={remove.isPending}>
                          <Trash2 />
                          Delete
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>{editingId ? 'Edit endpoint' : 'New endpoint'}</CardTitle>
          <CardDescription>
            {editingId
              ? 'The slug is the identity and cannot change; edit everything else.'
              : 'Pick a slug first — the call example above follows what you type.'}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
            <Input placeholder="slug, e.g. mobile-chat" value={form.slug} disabled={editingId !== null} onChange={(e) => set({ slug: e.target.value })} />
            <Input placeholder="display name (defaults to slug)" value={form.name} onChange={(e) => set({ name: e.target.value })} />
            <Input placeholder="description (optional)" value={form.description} onChange={(e) => set({ description: e.target.value })} />
          </div>
          <div className="space-y-2 rounded-md border border-border p-3">
            <p className="text-xs font-medium">Which models can serve this scope?</p>
            <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
              <Select
                value={form.model_mode}
                onChange={(e) => set({ model_mode: e.target.value as EndpointFormState['model_mode'] })}
                aria-label="model scope"
              >
                <option value="all">All models, all providers</option>
                <option value="provider">All models from one provider</option>
                <option value="single">One specific model</option>
                <option value="custom">Custom lists (advanced)</option>
              </Select>
              {form.model_mode === 'provider' || form.model_mode === 'single' ? (
                <Select
                  value={form.scope_provider}
                  onChange={(e) => set({ scope_provider: e.target.value, scope_model: '' })}
                  aria-label="scope provider"
                >
                  <option value="">pick a provider…</option>
                  {providerList.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              ) : null}
              {form.model_mode === 'single' ? (
                <Select
                  value={form.scope_model}
                  onChange={(e) => set({ scope_model: e.target.value })}
                  aria-label="scope model"
                  disabled={!form.scope_provider}
                >
                  <option value="">{form.scope_provider ? 'pick a model…' : 'pick a provider first'}</option>
                  {modelsForProvider.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </Select>
              ) : null}
            </div>
            {form.model_mode === 'custom' ? (
              <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
                <Input
                  placeholder="models, comma separated (names or patterns)"
                  value={form.custom_models}
                  onChange={(e) => set({ custom_models: e.target.value })}
                />
                <Input
                  placeholder="providers, comma separated"
                  value={form.custom_providers}
                  onChange={(e) => set({ custom_providers: e.target.value })}
                />
              </div>
            ) : (
              <p className="text-[11px] text-muted-foreground">
                {form.model_mode === 'all' && 'Every registered model stays eligible; the policy picks.'}
                {form.model_mode === 'provider' &&
                  (form.scope_provider
                    ? `Only ${providerById.get(form.scope_provider)?.name ?? 'the picked provider'} serves this scope.`
                    : 'Only the picked provider will serve this scope.')}
                {form.model_mode === 'single' && 'Exactly one model serves this scope (pinned).'}
              </p>
            )}
          </div>
          <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
            <Select value={form.strategy} onChange={(e) => set({ strategy: e.target.value })} aria-label="strategy">
              <option value="">strategy: policy default</option>
              {STRATEGY_OPTIONS.filter((s) => s).map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
            <Select value={form.use_cache} onChange={(e) => set({ use_cache: e.target.value })} aria-label="cache policy">
              <option value="">cache: policy default</option>
              <option value="true">cache: use</option>
              <option value="false">cache: bypass</option>
            </Select>
            <Input placeholder="max cost USD per request (optional)" value={form.max_cost_usd} onChange={(e) => set({ max_cost_usd: e.target.value })} />
          </div>
          <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
            <Input placeholder="latency target ms (optional)" value={form.latency_target_ms} onChange={(e) => set({ latency_target_ms: e.target.value })} />
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <input type="checkbox" checked={form.block_fallback} onChange={(e) => set({ block_fallback: e.target.checked })} />
              Block fallback for this scope
            </label>
          </div>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
            Enabled
          </label>
          {formError ? <p className="text-xs text-danger">{formError}</p> : null}
          <div className="flex gap-2">
            <Button size="sm" disabled={(!form.slug.trim() && editingId === null) || upsert.isPending} onClick={onSave}>
              {upsert.isPending ? 'Saving…' : editingId ? 'Save changes' : 'Create endpoint'}
            </Button>
            {editingId ? (
              <Button variant="ghost" size="sm" onClick={onCancel}>
                Cancel
              </Button>
            ) : null}
          </div>
        </CardContent>
      </Card>
    </>
  );
}
