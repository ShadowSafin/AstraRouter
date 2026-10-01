'use client';

import { Coins, RefreshCw } from 'lucide-react';

import { PageHeader } from '@/components/page-header';
import { TenantPicker, useTenantParam } from '@/components/tenant-picker';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/ui/state';
import { useBudgets } from '@/hooks/use-admin';
import { formatCurrency, formatDateTime, formatPercent } from '@/lib/format';
import { cn } from '@/lib/utils';
import type { Budget } from '@/lib/types';

function BudgetCard({ budget }: { budget: Budget }) {
  const remaining = Math.max(0, budget.limit_usd - budget.spent_usd);
  const consumed = budget.limit_usd > 0 ? budget.spent_usd / budget.limit_usd : 0;
  const over = budget.spent_usd > budget.limit_usd;

  // Three states, not two: a budget crossing an alert threshold is materially
  // different from one that is merely in use, and both are different from one
  // that has stopped serving traffic.
  const tone = over ? 'danger' : consumed > 0.8 ? 'warning' : 'success';

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2 text-base">
              {budget.scope}
              {budget.scope_id ? <span className="font-mono text-xs text-muted-foreground">{budget.scope_id}</span> : null}
              <Badge tone="outline">{budget.period}</Badge>
              <Badge tone={budget.enforced ? 'danger' : 'neutral'}>
                {budget.enforced ? 'enforced' : 'observational'}
              </Badge>
            </CardTitle>
            <CardDescription className="tabular-nums">
              {formatCurrency(budget.spent_usd)} of {formatCurrency(budget.limit_usd)} · resets {formatDateTime(budget.reset_at)}
            </CardDescription>
          </div>
          <div className="text-right">
            <p className={cn('text-lg font-semibold tabular-nums', over ? 'text-danger' : undefined)}>
              {formatCurrency(remaining)}
            </p>
            <p className="text-xs text-muted-foreground">remaining</p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
          <div
            className={cn('h-full rounded-full', tone === 'danger' ? 'bg-danger' : tone === 'warning' ? 'bg-warning' : 'bg-success')}
            // Clamped at 100% so an overspent budget does not overflow its track
            // and break the layout; the overage is stated in text instead.
            style={{ width: `${Math.min(100, consumed * 100)}%` }}
          />
        </div>
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
          <span>{formatPercent(consumed)} of the limit consumed</span>
          {(budget.alert_thresholds_usd ?? []).length > 0 ? (
            <span>alerts at {(budget.alert_thresholds_usd ?? []).map((value) => formatCurrency(value)).join(', ')}</span>
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}

export function BudgetsView() {
  const tenantId = useTenantParam();
  const { data: budgets, isPending, isError, error, refetch, isFetching } = useBudgets(tenantId);

  return (
    <>
      <PageHeader
        title="Budgets"
        description="Spend ceilings. Spend is maintained in Redis for a fast check and reconciled to Postgres, so the figure here is what enforcement is currently acting on rather than a billing statement."
        actions={
          <>
            <TenantPicker value={tenantId} />
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={isFetching ? 'animate-spin' : undefined} />
              Refresh
            </Button>
          </>
        }
        note="Select a tenant to list its budgets; the API scopes budget reads per tenant."
      />

      {isError ? <ErrorState error={error} onRetry={() => void refetch()} /> : null}

      {isPending ? (
        <TableSkeleton rows={3} columns={3} />
      ) : (budgets ?? []).length === 0 ? (
        <EmptyState
          title={tenantId ? 'No budgets for this tenant' : 'Select a tenant'}
          description={
            tenantId
              ? 'This tenant has no spend ceiling configured, so requests are limited only by the policy rate limits.'
              : 'Choose a tenant above to see its budgets. Budgets are always scoped to a tenant.'
          }
          action={
            <span className="mt-2 inline-flex items-center gap-1 text-xs text-muted-foreground">
              <Coins className="size-3.5" />
              Budgets are created through the admin API or the bootstrap config
            </span>
          }
        />
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {(budgets ?? []).map((budget) => (
            <BudgetCard key={budget.id} budget={budget} />
          ))}
        </div>
      )}
    </>
  );
}
