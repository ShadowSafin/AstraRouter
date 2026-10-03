'use client';

/**
 * The Advanced slide-over.
 *
 * The page itself is only a chat; everything else — credential and transport,
 * target, sampling, tools, routing constraints, compare lane, route and debug
 * metadata, the request as curl, session history — lives here behind the top
 * bar's Advanced button. Sections reuse the same components as the old console
 * layout (`ConfigPanel`, `AdvancedFields`, `MetadataPanel`, `HistoryList`), so
 * hiding them changes where they render, not what they do.
 */
import { Check, Columns2, Copy, Plus, SlidersHorizontal, X } from 'lucide-react';
import * as React from 'react';

import { AdvancedFields, advancedSummary } from '@/components/playground/advanced-popover';
import { ConfigPanel } from '@/components/playground/config-panel';
import { Field, Toggle } from '@/components/playground/controls';
import { HistoryList } from '@/components/playground/history-list';
import { MetadataPanel } from '@/components/playground/metadata-panel';
import { Input, Select } from '@/components/ui/input';
import {
  buildCurl,
  type PlaygroundConfig,
  type PlaygroundMessage,
  type PlaygroundRun,
  type StreamState,
} from '@/lib/playground';
import type { Endpoint, Model, RoutingPolicy } from '@/lib/types';
import type { CompareTarget } from '@/components/views/playground-view';
import { cn } from '@/lib/utils';

export interface AdvancedDrawerProps {
  open: boolean;
  onClose: () => void;
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
  compareOn: boolean;
  onCompareChange: (value: boolean) => void;
  onNewChat: () => void;
  newChatDisabled: boolean;
  compareTarget: CompareTarget;
  onCompareTarget: (patch: Partial<CompareTarget>) => void;
  onRunAll: () => void;
  runAllDisabled: boolean;
  latestRun: PlaygroundRun | null;
  partial: StreamState;
  running: boolean;
  gatewayUrl: string;
  messages: PlaygroundMessage[];
  runs: PlaygroundRun[];
  onLoad: (run: PlaygroundRun) => void;
  onRerun: (run: PlaygroundRun) => void;
  onClearHistory: () => void;
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <p className="mb-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
      {children}
    </p>
  );
}

export function AdvancedDrawer(props: AdvancedDrawerProps) {
  const { open, onClose } = props;
  const [curlCopied, setCurlCopied] = React.useState(false);

  // Escape closes; the background locks so the chat underneath stops scrolling.
  React.useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    const previous = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = previous;
    };
  }, [open, onClose]);

  const curl = React.useMemo(
    () => buildCurl(props.config, props.messages, props.gatewayUrl),
    [props.config, props.messages, props.gatewayUrl],
  );

  return (
    <>
      <div
        aria-hidden={!open}
        onClick={onClose}
        className={cn(
          'fixed inset-0 z-40 bg-black/55 backdrop-blur-[2px] transition-opacity duration-300',
          open ? 'opacity-100' : 'pointer-events-none opacity-0',
        )}
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="Advanced playground settings"
        aria-hidden={!open}
        className={cn(
          'fixed inset-y-0 right-0 z-50 flex w-[min(94vw,430px)] flex-col border-l border-white/10 bg-[#0b0b10] shadow-2xl transition-transform duration-300 ease-out',
          open ? 'translate-x-0' : 'translate-x-full',
        )}
      >
        <div className="flex items-center gap-2 border-b border-white/[0.07] px-4 py-3">
          <SlidersHorizontal className="size-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <p className="text-[13px] font-semibold text-foreground">Advanced</p>
            <p className="truncate text-[11px] text-muted-foreground">{advancedSummary(props.config)}</p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close advanced settings"
            className="flex size-8 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
          <section>
            <SectionTitle>Session</SectionTitle>
            <div className="space-y-3 rounded-xl border border-white/[0.07] bg-white/[0.015] p-3">
              <Toggle
                label="Compare mode"
                hint="Run the same conversation against two targets and compare the results."
                checked={props.compareOn}
                onChange={props.onCompareChange}
                disabled={props.disabled}
              />
              <button
                type="button"
                onClick={props.onNewChat}
                disabled={props.newChatDisabled}
                title="Start a fresh thread"
                className="flex h-8 w-full items-center justify-center gap-1.5 rounded-lg bg-white/[0.08] px-3 text-xs font-medium text-foreground transition-colors hover:bg-white/[0.12] disabled:opacity-40"
              >
                <Plus className="size-3.5" />
                New chat
              </button>
            </div>
          </section>

          <section>
            <SectionTitle>Connection &amp; target</SectionTitle>
            <ConfigPanel
              config={props.config}
              onChange={props.onChange}
              apiKey={props.apiKey}
              onApiKeyChange={props.onApiKeyChange}
              models={props.models}
              modelsPending={props.modelsPending}
              endpoints={props.endpoints}
              endpointsPending={props.endpointsPending}
              policies={props.policies}
              policiesPending={props.policiesPending}
              providerFilter={props.providerFilter}
              onProviderFilterChange={props.onProviderFilterChange}
              disabled={props.disabled}
            />
          </section>

          <section>
            <SectionTitle>Sampling, tools &amp; routing</SectionTitle>
            <AdvancedFields
              config={props.config}
              onChange={props.onChange}
              disabled={props.disabled}
            />
          </section>

          {props.compareOn ? (
            <section>
              <SectionTitle>Lane B — compare target</SectionTitle>
              <div className="space-y-3 rounded-xl border border-white/[0.07] bg-white/[0.015] p-3">
                <Field
                  label="Model"
                  hint="Also selectable as lane B above the composer; typing here accepts registry names and aliases."
                >
                  <Input
                    list="playground-compare-models"
                    placeholder="e.g. another-model"
                    value={props.compareTarget.model}
                    onChange={(event) =>
                      props.onCompareTarget({ model: event.target.value })
                    }
                    spellCheck={false}
                  />
                  <datalist id="playground-compare-models">
                    {props.models.map((model) => (
                      <option key={model.id} value={model.name}>
                        {model.provider_name ?? model.provider_id}
                      </option>
                    ))}
                  </datalist>
                </Field>
                <Field label="Endpoint scope" hint="Provider preference for lane B.">
                  <Select
                    value={props.compareTarget.endpoint}
                    onChange={(event) =>
                      props.onCompareTarget({ endpoint: event.target.value })
                    }
                  >
                    <option value="">Same as lane A</option>
                    {props.endpoints.map((endpoint) => (
                      <option key={endpoint.id} value={endpoint.slug}>
                        {endpoint.slug}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label="Routing policy">
                  <Select
                    value={props.compareTarget.policy}
                    onChange={(event) =>
                      props.onCompareTarget({ policy: event.target.value })
                    }
                  >
                    <option value="">Same as lane A</option>
                    {props.policies.map((policy) => (
                      <option key={policy.id} value={policy.id}>
                        {policy.name}
                      </option>
                    ))}
                  </Select>
                </Field>
                <button
                  type="button"
                  onClick={props.onRunAll}
                  disabled={props.runAllDisabled}
                  title="Run the conversation against both lanes"
                  className="flex h-8 w-full items-center justify-center gap-1.5 rounded-lg bg-white/[0.08] px-3 text-xs font-medium text-foreground transition-colors hover:bg-white/[0.12] disabled:opacity-40"
                >
                  <Columns2 className="size-3.5" />
                  Run both lanes
                </button>
              </div>
            </section>
          ) : null}

          <section>
            <SectionTitle>Route &amp; debug</SectionTitle>
            <MetadataPanel
              config={props.config}
              run={props.latestRun}
              partial={props.partial}
              running={props.running}
            />
          </section>

          <section>
            <SectionTitle>Request as curl</SectionTitle>
            <div className="overflow-hidden rounded-xl border border-white/[0.07] bg-white/[0.015]">
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words p-3 font-mono text-[10px] leading-relaxed text-neutral-300">
                {curl}
              </pre>
              <div className="border-t border-white/[0.06] px-2 py-1.5">
                <button
                  type="button"
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(curl)
                      .then(() => {
                        setCurlCopied(true);
                        window.setTimeout(() => setCurlCopied(false), 1500);
                      })
                      .catch(() => undefined);
                  }}
                  className="flex h-7 items-center gap-1.5 rounded-lg px-2 text-[11px] font-medium text-neutral-300 transition-colors hover:bg-white/[0.06] hover:text-white"
                >
                  {curlCopied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
                  {curlCopied ? 'Copied' : 'Copy as curl'}
                </button>
              </div>
            </div>
          </section>

          <section>
            <SectionTitle>Session history</SectionTitle>
            <div className="overflow-hidden rounded-xl border border-white/[0.07] bg-white/[0.015]">
              <HistoryList
                runs={props.runs}
                onLoad={props.onLoad}
                onRerun={props.onRerun}
                onClear={props.onClearHistory}
                disabled={props.disabled}
              />
            </div>
          </section>
        </div>
      </aside>
    </>
  );
}
