'use client';

/**
 * The Playground's slim configuration column.
 *
 * Deliberately short: credential and mode, then the target. Everything else
 * moved to the Advanced popover on the composer, because a console whose left
 * column is a wall of inputs pushes the thing the operator actually came to do —
 * send a prompt — off the screen.
 *
 * The model is shown here but *edited* in the composer's model menu, so the
 * control and the target can never disagree about which model will be sent.
 */
import { Info, KeyRound, Link2 } from 'lucide-react';
import * as React from 'react';

import { Field, SectionCard, Toggle } from '@/components/playground/controls';
import { Input, Select } from '@/components/ui/input';
import type { PlaygroundConfig } from '@/lib/playground';
import type { Endpoint, Model, RoutingPolicy } from '@/lib/types';
import { cn } from '@/lib/utils';

/** A one-line description of what an endpoint scope will do to routing. */
export function overrideSummary(endpoint: Endpoint | undefined): string {
  const override = endpoint?.routing_override;
  if (!endpoint) return 'the policy decides everything';
  if (!override) return 'no routing override';
  const parts: string[] = [];
  if (override.force_model) parts.push(`forces ${override.force_model}`);
  if (override.preferred_providers?.length)
    parts.push(`prefers providers ${override.preferred_providers.join(', ')}`);
  if (override.preferred_models?.length)
    parts.push(`prefers models ${override.preferred_models.join(', ')}`);
  if (override.strategy) parts.push(`strategy ${override.strategy}`);
  if (override.max_cost_usd != null) parts.push(`max $${override.max_cost_usd}`);
  if (override.latency_target_ms != null) parts.push(`target ${override.latency_target_ms}ms`);
  if (override.block_fallback) parts.push('no fallback');
  if (override.use_cache === false) parts.push('cache off');
  return parts.length > 0 ? parts.join(' · ') : 'no routing override';
}

export interface ConfigPanelProps {
  config: PlaygroundConfig;
  onChange: (patch: Partial<PlaygroundConfig>) => void;
  apiKey: string;
  onApiKeyChange: (value: string) => void;
  models: Model[];
  modelsPending: boolean;
  endpoints: Endpoint[];
  endpointsPending: boolean;
  policies: RoutingPolicy[];
  policiesPending: boolean;
  providerFilter: string;
  onProviderFilterChange: (value: string) => void;
  disabled: boolean;
}

export function ConfigPanel({
  config,
  onChange,
  apiKey,
  onApiKeyChange,
  models,
  modelsPending,
  endpoints,
  endpointsPending,
  policies,
  policiesPending,
  providerFilter,
  onProviderFilterChange,
  disabled,
}: ConfigPanelProps) {
  const providers = React.useMemo(() => {
    const seen = new Map<string, string>();
    for (const model of models) {
      if (model.provider_id && !seen.has(model.provider_id)) {
        seen.set(model.provider_id, model.provider_name ?? model.provider_id);
      }
    }
    return [...seen.entries()].map(([id, name]) => ({ id, name }));
  }, [models]);

  const selectedEndpoint = endpoints.find((endpoint) => endpoint.slug === config.endpoint.trim());
  const model = models.find((item) => item.name === config.model.trim());

  return (
    <div className="space-y-2.5">
      <SectionCard title="Transport" summary={apiKey.trim() ? 'key set' : 'no key'} defaultOpen>
        <Field
          label="API key"
          hint="Sent as a bearer token on the run and used once. It is never stored — not in history, not on disk."
        >
          <Input
            type="password"
            autoComplete="off"
            spellCheck={false}
            placeholder="cr_live_…"
            value={apiKey}
            onChange={(event) => onApiKeyChange(event.target.value)}
          />
        </Field>
        <Toggle
          label="Streaming"
          hint="Requests SSE and renders chunks as they arrive. Off shows one complete response."
          checked={config.streaming}
          onChange={(next) => onChange({ streaming: next })}
          disabled={disabled}
        />
        <Toggle
          label="Debug metadata"
          hint="Adds X-CoreRouter-Debug, which restores the routing decision and trace id on the response."
          checked={config.debug}
          onChange={(next) => onChange({ debug: next })}
          disabled={disabled}
        />
      </SectionCard>

      <SectionCard title="Target" summary={config.model.trim() || 'no model yet'} defaultOpen>
        <Field
          label="Endpoint scope"
          hint={
            <>
              Sent as <code className="font-mono">X-CoreRouter-Endpoint</code>. This is how a
              provider preference is applied: the scope&apos;s override folds into the policy for one
              request.
            </>
          }
        >
          <Select
            value={config.endpoint}
            onChange={(event) => onChange({ endpoint: event.target.value })}
            disabled={disabled || endpointsPending}
          >
            <option value="">None — let the policy decide</option>
            {endpoints.map((endpoint) => (
              <option key={endpoint.id} value={endpoint.slug}>
                {endpoint.slug} — {endpoint.name}
                {endpoint.enabled ? '' : ' (disabled)'}
              </option>
            ))}
          </Select>
        </Field>
        {config.endpoint.trim() ? (
          <p className="flex items-start gap-1.5 text-[11px] text-muted-foreground">
            <Info className="mt-px size-3 shrink-0" />
            <span>{overrideSummary(selectedEndpoint)}</span>
          </p>
        ) : null}

        <div className="rounded-lg border border-white/[0.07] bg-white/[0.02] px-3 py-2">
          <p className="text-[10px] uppercase tracking-wide text-muted-foreground">Model</p>
          <p className="mt-0.5 truncate font-mono text-[12px] text-foreground" title={config.model}>
            {config.model.trim() || (modelsPending ? 'loading models…' : 'not selected yet')}
          </p>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {model ? (
              <>
                {model.provider_name ?? model.provider_id} · {model.context_window.toLocaleString()} ctx
                {model.status === 'active' ? '' : ` · ${model.status}`}
              </>
            ) : config.model.trim() ? (
              'Not in the registry — the gateway may still resolve it by alias, or reject it.'
            ) : (
              'Pick one from the model menu in the composer.'
            )}
          </p>
        </div>

        <Field label="Provider filter" hint="Narrows the composer's model menu. It does not pin routing.">
          <Select
            value={providerFilter}
            onChange={(event) => onProviderFilterChange(event.target.value)}
          >
            <option value="">All providers</option>
            {providers.map((provider) => (
              <option key={provider.id} value={provider.id}>
                {provider.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="Routing policy"
          hint={
            <>
              Sent as <code className="font-mono">X-CoreRouter-Policy</code>. Pins the rule set
              instead of letting the key or tenant default decide.
            </>
          }
        >
          <Select
            value={config.policy}
            onChange={(event) => onChange({ policy: event.target.value })}
            disabled={disabled || policiesPending}
          >
            <option value="">Default for this key</option>
            {policies.map((policy) => (
              <option key={policy.id} value={policy.id}>
                {policy.name}
                {policy.enabled ? '' : ' (disabled)'}
              </option>
            ))}
          </Select>
        </Field>
      </SectionCard>

      <div
        className={cn(
          'flex items-start gap-2 rounded-xl border px-3 py-2.5 text-[11px] leading-relaxed',
          apiKey.trim()
            ? 'border-white/[0.07] bg-white/[0.015] text-muted-foreground'
            : 'border-warning/30 bg-warning/[0.06] text-warning',
        )}
      >
        {apiKey.trim() ? (
          <Link2 className="mt-px size-3.5 shrink-0" />
        ) : (
          <KeyRound className="mt-px size-3.5 shrink-0" />
        )}
        <span>
          {apiKey.trim() ? (
            <>
              A key is set for this session. The run is authenticated as the tenant that owns it, so
              its policies, budgets and cache namespace apply.
            </>
          ) : (
            <>
              No key yet. The inference API authenticates with a tenant key, so a run cannot be sent
              without one. Mint one on{' '}
              <a href="/keys" className="underline underline-offset-2">
                API keys
              </a>
              .
            </>
          )}
        </span>
      </div>

      {!endpointsPending && endpoints.length === 0 ? (
        <p className="px-1 text-[11px] leading-relaxed text-muted-foreground">
          No endpoint scopes are configured. That is fine — the default policy routes these runs. Add
          a scope on the Endpoints page when you need to test a provider preference.
        </p>
      ) : null}
    </div>
  );
}
