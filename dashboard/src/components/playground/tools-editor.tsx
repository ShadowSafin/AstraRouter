'use client';

/**
 * Attach tools to a run.
 *
 * The inference API validates `tools` before routing and refuses a malformed
 * declaration, so the Playground shows the parse error rather than sending a
 * request that cannot succeed. Tools can be typed by hand or attached from the
 * registry, which is what makes this useful for verifying that a real tool
 * executes and comes back as a tool call rather than only that the model can
 * imagine one.
 */
import { Plus, Wrench, X } from 'lucide-react';
import * as React from 'react';

import { Field, Textarea } from '@/components/playground/controls';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useTools } from '@/hooks/use-admin';
import type { Tool } from '@/lib/types';

/** The OpenAI function-tool declaration the gateway expects. */
type DeclaredTool = { type: 'function'; function: Record<string, unknown> };

function parseTools(raw: string): { tools: DeclaredTool[]; error: string | null } {
  if (!raw.trim()) return { tools: [], error: null };
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (cause) {
    return { tools: [], error: cause instanceof Error ? cause.message : 'invalid JSON' };
  }
  if (!Array.isArray(parsed)) return { tools: [], error: 'must be a JSON array' };
  return { tools: parsed as DeclaredTool[], error: null };
}

/** Build a function declaration from a registry tool. */
function declarationFor(tool: Tool): DeclaredTool {
  return {
    type: 'function',
    function: {
      name: tool.name,
      description: tool.description ?? '',
      parameters: tool.parameters ?? { type: 'object', properties: {} },
    },
  };
}

export function ToolsEditor({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (next: string) => void;
  disabled: boolean;
}) {
  const { data: registry, isPending } = useTools();
  const { tools, error } = React.useMemo(() => parseTools(value), [value]);

  const attached = new Set(
    tools
      .map((tool) => tool?.function?.name)
      .filter((name): name is string => typeof name === 'string'),
  );

  const attach = (tool: Tool) => {
    onChange(JSON.stringify([...tools, declarationFor(tool)], null, 2));
  };

  const detach = (name: string) => {
    const next = tools.filter((tool) => tool?.function?.name !== name);
    onChange(next.length > 0 ? JSON.stringify(next, null, 2) : '');
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Attached to this run
        </p>
        {tools.length > 0 ? (
          <Button variant="ghost" size="sm" onClick={() => onChange('')} disabled={disabled}>
            <X />
            Clear
          </Button>
        ) : (
          <Badge tone="neutral">none</Badge>
        )}
      </div>

      {tools.length > 0 ? (
        <ul className="flex flex-wrap gap-1.5">
          {tools.map((tool, index) => {
            const name = tool?.function?.name;
            return (
              <li key={`${String(name)}-${index}`}>
                <button
                  type="button"
                  onClick={() => (typeof name === 'string' ? detach(name) : undefined)}
                  className="inline-flex items-center gap-1.5 rounded-full border border-white/[0.1] bg-white/[0.03] px-2.5 py-1 font-mono text-[11px] text-neutral-200 transition-colors hover:border-danger/30 hover:text-danger"
                  title={typeof name === 'string' ? `Remove ${name}` : 'Remove'}
                >
                  <Wrench className="size-3" />
                  {typeof name === 'string' ? name : 'unnamed'}
                  <X className="size-3" />
                </button>
              </li>
            );
          })}
        </ul>
      ) : null}

      <Field
        label="tools"
        hint={
          error ? (
            <span className="text-danger">Not usable yet: {error}</span>
          ) : (
            'The gateway validates this array before routing; a malformed declaration is rejected with a 400 naming the tool.'
          )
        }
      >
        <Textarea
          rows={4}
          spellCheck={false}
          placeholder='[{"type":"function","function":{"name":"get_time"}}]'
          value={value}
          onChange={(event) => onChange(event.target.value)}
        />
      </Field>

      <div className="space-y-1.5">
        <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          From the registry
        </p>
        {isPending ? (
          <p className="text-[11px] text-muted-foreground">loading tools…</p>
        ) : (registry ?? []).length === 0 ? (
          <p className="text-[11px] text-muted-foreground">
            No tools are registered. Define one on the Tool registry page to test tool calling here.
          </p>
        ) : (
          <ul className="divide-y divide-white/[0.05] overflow-hidden rounded-lg border border-white/[0.07]">
            {(registry ?? []).map((tool) => (
              <li key={tool.id} className="flex items-center gap-2 px-2.5 py-2">
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5">
                    <span className="truncate font-mono text-[11px] text-neutral-200">{tool.name}</span>
                    <Badge tone={tool.safety_level === 'dangerous' ? 'danger' : tool.safety_level === 'sensitive' ? 'warning' : 'neutral'}>
                      {tool.safety_level}
                    </Badge>
                    {tool.kind === 'external' ? <Badge tone="info">external</Badge> : null}
                  </span>
                  {tool.description ? (
                    <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                      {tool.description}
                    </span>
                  ) : null}
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={disabled || !tool.enabled || attached.has(tool.name)}
                  title={
                    !tool.enabled
                      ? 'This tool is disabled'
                      : attached.has(tool.name)
                        ? 'Already attached'
                        : `Attach ${tool.name}`
                  }
                  onClick={() => attach(tool)}
                >
                  <Plus />
                  {attached.has(tool.name) ? 'Attached' : 'Attach'}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <p className="text-[11px] leading-relaxed text-muted-foreground">
        A tool the gateway runs comes back as a tool call, and its result is reported as a tool
        invocation — <span className="font-mono">executed</span> is the success status. Tool
        routing still passes through the policy engine, so a denied tool is denied here too.
      </p>
    </div>
  );
}
