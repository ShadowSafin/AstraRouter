'use client';

import { Cpu, Database, GitBranch, History, RefreshCw, ServerCog, ShieldCheck } from 'lucide-react';

import { PageHeader } from '@/components/page-header';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useAudit, useSystem } from '@/hooks/use-admin';
import { formatDateTime, formatNumber } from '@/lib/format';

/** Render an arbitrary diagnostic value without assuming its shape. */
function JsonBlock({ value }: { value: unknown }) {
  return (
    <pre className="max-h-64 overflow-auto rounded-xl border border-white/[0.07] bg-white/[0.02] p-3 font-mono text-[11px] leading-relaxed text-neutral-300">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

function Fact({ label, value, mono = false }: { label: string; value: React.ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-white/[0.05] py-2.5 last:border-0">
      <span className="text-xs text-neutral-400">{label}</span>
      <span className={mono ? 'font-mono text-xs text-neutral-200' : 'text-xs font-medium text-white'}>{value}</span>
    </div>
  );
}

export function SettingsView() {
  const system = useSystem();
  const audit = useAudit(50);

  const version = system.data?.version;
  const runtime = system.data?.runtime;
  const components = Object.entries(system.data?.components ?? {});

  return (
    <>
      <PageHeader
        title="Settings"
        description="Build, runtime and dependency status. This is a read-only view: configuration lives in the gateway's config file and environment, not in the dashboard — an operator should never be one click away from changing how traffic is routed."
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              void system.refetch();
              void audit.refetch();
            }}
            disabled={system.isFetching || audit.isFetching}
          >
            <RefreshCw className={system.isFetching || audit.isFetching ? 'animate-spin' : undefined} />
            Refresh
          </Button>
        }
      />

      {system.isError ? <ErrorState error={system.error} onRetry={() => void system.refetch()} /> : null}

      {system.isPending ? (
        <TableSkeleton rows={6} columns={2} />
      ) : system.data ? (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <GitBranch className="size-4" />
                Build
              </CardTitle>
              <CardDescription>Stamped into the binary at build time and exported on corerouter_build_info.</CardDescription>
            </CardHeader>
            <CardContent className="pt-0">
              <Fact label="Version" value={version?.version || 'unknown'} mono />
              <Fact label="Commit" value={version?.commit || 'unknown'} mono />
              <Fact
                label="Build date"
                value={version?.build_date && version.build_date !== 'unknown' ? formatDateTime(version.build_date) : 'unknown'}
              />
              <Fact
                label="Working tree"
                value={version?.dirty ? <Badge tone="warning" dot>dirty</Badge> : <Badge tone="success" dot>clean</Badge>}
              />
              <Fact label="Go" value={version?.go_version || runtime?.go_version || '—'} mono />
              <Fact label="Uptime" value={system.data.uptime || '—'} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Cpu className="size-4" />
                Runtime
              </CardTitle>
              <CardDescription>Live process figures. A steadily rising goroutine count is the earliest leak signal.</CardDescription>
            </CardHeader>
            <CardContent className="pt-0">
              <Fact label="Goroutines" value={formatNumber(runtime?.goroutines ?? 0)} />
              <Fact label="CPUs" value={`${formatNumber(runtime?.num_cpu ?? 0)} available · GOMAXPROCS ${formatNumber(runtime?.gomaxprocs ?? 0)}`} />
              <Fact label="Providers configured" value={formatNumber((system.data.providers ?? []).length)} />
            </CardContent>
          </Card>

          <Card className="lg:col-span-2">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Database className="size-4" />
                Dependencies
              </CardTitle>
              <CardDescription>
                Connection state as each client reports it. A required dependency being down makes the gateway report
                itself unready, so a load balancer drains it instead of serving errors.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              {components.length === 0 ? (
                <p className="text-xs text-muted-foreground">No dependency states reported.</p>
              ) : (
                components.map(([name, value]) => (
                  <div key={name} className="space-y-1.5">
                    <p className="flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      <ServerCog className="size-3.5" />
                      {name}
                    </p>
                    <JsonBlock value={value} />
                  </div>
                ))
              )}
            </CardContent>
          </Card>

          <Card className="lg:col-span-2">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <ShieldCheck className="size-4" />
                Provider readiness
              </CardTitle>
              <CardDescription>
                Configuration is not the same as usability: a provider with a missing credential is configured but has
                no adapter and is excluded from routing.
              </CardDescription>
            </CardHeader>
            <CardContent className="p-0">
              {(system.data.providers ?? []).length === 0 ? (
                <p className="p-5 text-xs text-muted-foreground">No providers configured.</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Provider</TableHead>
                      <TableHead>Kind</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Adapter</TableHead>
                      <TableHead>Health</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(system.data.providers ?? []).map((provider) => (
                      <TableRow key={provider.id}>
                        <TableCell className="text-sm">{provider.name}</TableCell>
                        <TableCell className="text-xs">{provider.kind}</TableCell>
                        <TableCell>
                          <Badge tone={provider.status === 'active' ? 'success' : 'neutral'}>{provider.status}</Badge>
                        </TableCell>
                        <TableCell>
                          <Badge tone={provider.adapter_ready ? 'info' : 'danger'}>
                            {provider.adapter_ready ? 'ready' : 'unavailable'}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs">{provider.health?.state ?? 'unknown'}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>

          <Card className="lg:col-span-2">
            <CardHeader>
              <CardTitle>Redacted effective configuration</CardTitle>
              <CardDescription>
                What the gateway is actually running with, with every secret replaced. Useful for confirming a value
                arrived from the environment rather than a config file.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <JsonBlock value={system.data.config} />
            </CardContent>
          </Card>
        </div>
      ) : null}

      <Card className="mt-4">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <History className="size-4" />
            Recent administrative activity
          </CardTitle>
          <CardDescription>
            Every control-plane mutation is audited. Inference traffic is not: its accountability need is satisfied by
            the usage records, and auditing it would dwarf the signal.
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {audit.isError ? (
            <div className="p-5">
              <ErrorState error={audit.error} onRetry={() => void audit.refetch()} />
            </div>
          ) : audit.isPending ? (
            <TableSkeleton rows={5} columns={4} />
          ) : (audit.data ?? []).length === 0 ? (
            <EmptyState
              title="No administrative events"
              description="Minting a key, editing a policy or probing a provider will appear here."
              className="m-5"
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>When</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Resource</TableHead>
                  <TableHead>Detail</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(audit.data ?? []).map((event) => (
                  <TableRow key={event.id}>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                      {formatDateTime(event.created_at)}
                    </TableCell>
                    <TableCell className="text-xs">{event.actor_label || event.actor_key_id || 'unknown'}</TableCell>
                    <TableCell>
                      <Badge
                        tone={
                          event.action === 'delete' || event.action === 'revoke'
                            ? 'danger'
                            : event.action === 'create'
                              ? 'success'
                              : 'info'
                        }
                      >
                        {event.action}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs">
                      {event.resource}
                      {event.resource_id ? (
                        <span className="ml-1 font-mono text-muted-foreground">{event.resource_id}</span>
                      ) : null}
                    </TableCell>
                    <TableCell className="max-w-md truncate font-mono text-[11px] text-muted-foreground">
                      {event.after ? JSON.stringify(event.after) : event.metadata ? JSON.stringify(event.metadata) : '—'}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
