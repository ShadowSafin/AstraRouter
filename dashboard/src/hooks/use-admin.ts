'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiFetch, buildQuery } from '@/lib/api';
import type {
  APIKey,
  AgentRun,
  AnalyticsReport,
  AgentStep,
  AuditEvent,
  Budget,
  CacheEntry,
  CacheInvalidation,
  CachePolicy,
  CachePolicyInput,
  CacheStats,
  CostAnomaliesResponse,
  CostBudgetsResponse,
  CostForecastResponse,
  CostGroupedResponse,
  CostOverviewResponse,
  CostRequestResponse,
  CostSavingsResponse,
  CostSeriesResponse,
  CostTopResponse,
  PricingListResponse,
  PricingVersion,
  TunnelSession,
  TunnelStatusResponse,
  CredentialMeta,
  Endpoint,
  ErrorsResponse,
  EvaluationResult,
  EvaluationRun,
  KeyUpdateInput,
  Model,
  ModelInput,
  ModelScore,
  Override,
  OverrideInput,
  Overview,
  PolicyInput,
  Provider,
  ProviderHealth,
  ProviderInput,
  ProviderScore,
  ProviderSummary,
  ProviderTestResponse,
  ProviderTestResult,
  ReplayJob,
  RequestLog,
  RoutingExplanation,
  RoutingPolicy,
  SystemResponse,
  Tenant,
  TenantInput,
  Tool,
  ToolInput,
  ToolInvocation,
  ToolPolicy,
  ToolPolicyInput,
  UsageSummaryResponse,
} from '@/lib/types';

/**
 * Query keys.
 *
 * A single factory keeps every key in one place, which is what makes targeted
 * invalidation after a mutation reliable: a hand-written string array at a call
 * site is one typo away from an invalidation that silently does nothing.
 */
export const queryKeys = {
  overview: (tenantId: string, window: string) => ['overview', tenantId, window] as const,
  analytics: (filters: Record<string, unknown>) => ['analytics', filters] as const,
  usageSummary: (tenantId: string, window: string) => ['usage-summary', tenantId, window] as const,
  requests: (filters: Record<string, unknown>) => ['requests', filters] as const,
  errors: (window: string) => ['errors', window] as const,
  providers: () => ['providers'] as const,
  providerHealth: () => ['provider-health'] as const,
  models: () => ['models'] as const,
  policies: () => ['policies'] as const,
  budgets: (tenantId: string) => ['budgets', tenantId] as const,
  tenants: () => ['tenants'] as const,
  keys: (tenantId: string) => ['keys', tenantId] as const,
  audit: (limit: number) => ['audit', limit] as const,
  system: () => ['system'] as const,
  scores: () => ['scores'] as const,
  cacheStats: (tenantId?: string) => ['cache-stats', tenantId ?? ''] as const,
  cachePolicies: (tenantId: string) => ['cache-policies', tenantId] as const,
  cacheInspect: (filters: Record<string, unknown>) => ['cache-inspect', filters] as const,
  cacheInvalidations: (tenantId: string) => ['cache-invalidations', tenantId] as const,
  replay: (tenantId: string) => ['replay', tenantId] as const,
  evals: (tenantId: string) => ['evals', tenantId] as const,
  evalDetail: (id: string) => ['eval-detail', id] as const,
  endpoints: () => ['endpoints'] as const,
  tools: () => ['tools'] as const,
  toolPolicies: () => ['tool-policies'] as const,
  toolInvocations: (limit: number) => ['tool-invocations', limit] as const,
  toolInvocationDetail: (id: string) => ['tool-invocation', id] as const,
  agentRuns: (limit: number) => ['agent-runs', limit] as const,
  agentRunDetail: (id: string) => ['agent-run', id] as const,
  explain: (requestId: string) => ['explain', requestId] as const,
  overrides: (limit: number) => ['overrides', limit] as const,
  providerTests: (providerId: string, limit: number) => ['provider-tests', providerId, limit] as const,
  cost: (filters: Record<string, unknown>) => ['cost', filters] as const,
  pricing: () => ['pricing'] as const,
};

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

export function useOverview(tenantId: string, from: string) {
  return useQuery({
    queryKey: queryKeys.overview(tenantId, from),
    queryFn: ({ signal }) =>
      apiFetch<Overview>(`overview${buildQuery({ tenant_id: tenantId, from })}`, { signal }),
  });
}

export interface AnalyticsFilters {
  tenantId: string;
  /** A Go duration such as "24h"; see windowToFromParam. */
  from: string;
  provider?: string;
  model?: string;
  /** Rows per ranking table. */
  top?: number;
  /** requests | cost | errors | latency | tokens */
  sort?: string;
}

/**
 * The composed analytics report for one window and filter set.
 *
 * One query rather than a dozen: every section of the analytics page must
 * describe the same window, and fetching them separately is how a dashboard ends
 * up with a KPI card from one minute and a chart from the next.
 */
export function useAnalyticsReport(filters: AnalyticsFilters) {
  return useQuery({
    queryKey: queryKeys.analytics(filters as unknown as Record<string, unknown>),
    queryFn: ({ signal }) =>
      apiFetch<AnalyticsReport>(
        `analytics/report${buildQuery({
          tenant_id: filters.tenantId,
          from: filters.from,
          provider: filters.provider,
          model: filters.model,
          top: filters.top,
          sort: filters.sort,
        })}`,
        { signal },
      ),
    // The server caches for ~20s; matching that here keeps the client from
    // re-fetching a window the gateway would serve from cache anyway.
    staleTime: 20_000,
  });
}

export function useUsageSummary(tenantId: string, from: string) {
  return useQuery({
    queryKey: queryKeys.usageSummary(tenantId, from),
    queryFn: ({ signal }) =>
      apiFetch<UsageSummaryResponse>(`usage/summary${buildQuery({ tenant_id: tenantId, from })}`, { signal }),
  });
}

export interface RequestFilters {
  tenantId: string;
  from: string;
  provider?: string;
  model?: string;
  outcome?: string;
  errorCode?: string;
  search?: string;
  limit?: number;
  offset?: number;
}

export function useRequests(filters: RequestFilters) {
  const params = {
    tenant_id: filters.tenantId,
    from: filters.from,
    provider: filters.provider,
    model: filters.model,
    outcome: filters.outcome,
    error_code: filters.errorCode,
    search: filters.search,
    limit: filters.limit ?? 50,
    offset: filters.offset ?? 0,
  };
  return useQuery({
    queryKey: queryKeys.requests(params),
    queryFn: ({ signal }) =>
      apiFetch<{ window: unknown; requests: RequestLog[] | null; limit: number; offset: number }>(
        `requests${buildQuery(params)}`,
        { signal },
      ),
    // The requests table is a live tail: poll while mounted so rows served
    // from another device appear without a manual refresh.
    refetchInterval: 10_000,
    // A page with thousands of matching rows is a UX problem, not a feature, so
    // the table holds the previous page's data while the next one loads rather
    // than collapsing to a spinner.
    placeholderData: (previous) => previous,
  });
}

export function useErrors(from: string) {
  return useQuery({
    queryKey: queryKeys.errors(from),
    queryFn: ({ signal }) => apiFetch<ErrorsResponse>(`errors${buildQuery({ from, limit: 100 })}`, { signal }),
  });
}

export function useProviders() {
  return useQuery({
    queryKey: queryKeys.providers(),
    queryFn: ({ signal }) =>
      apiFetch<{ providers: ProviderSummary[] | null }>('providers', { signal }).then((data) => data.providers ?? []),
  });
}

export function useProviderHealth() {
  return useQuery({
    queryKey: queryKeys.providerHealth(),
    queryFn: ({ signal }) =>
      apiFetch<{ health: ProviderHealth[] | null }>('providers/health', { signal }).then(
        (data) => data.health ?? [],
      ),
  });
}

export function useModels() {
  return useQuery({
    queryKey: queryKeys.models(),
    queryFn: ({ signal }) =>
      apiFetch<{ models: Model[] | null }>('models', { signal }).then((data) => data.models ?? []),
  });
}

export function usePolicies() {
  return useQuery({
    queryKey: queryKeys.policies(),
    queryFn: ({ signal }) =>
      apiFetch<{ policies: RoutingPolicy[] | null }>('policies', { signal }).then(
        (data) => data.policies ?? [],
      ),
  });
}

export function useBudgets(tenantId: string) {
  return useQuery({
    queryKey: queryKeys.budgets(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<{ budgets: Budget[] | null }>(`budgets${buildQuery({ tenant_id: tenantId })}`, { signal }).then(
        (data) => data.budgets ?? [],
      ),
  });
}

export function useTenants() {
  return useQuery({
    queryKey: queryKeys.tenants(),
    queryFn: ({ signal }) =>
      apiFetch<{ tenants: Tenant[] | null }>('tenants', { signal }).then((data) => data.tenants ?? []),
  });
}

export function useKeys(tenantId: string) {
  return useQuery({
    queryKey: queryKeys.keys(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<{ keys: APIKey[] | null }>(`keys${buildQuery({ tenant_id: tenantId })}`, { signal }).then(
        (data) => data.keys ?? [],
      ),
    // The keys endpoint requires a tenant id; without one it returns a 400, so
    // the query is held until a tenant is known.
    enabled: tenantId.length > 0,
  });
}

export function useAudit(limit = 100) {
  return useQuery({
    queryKey: queryKeys.audit(limit),
    queryFn: ({ signal }) =>
      apiFetch<{ events: AuditEvent[] | null }>(`audit${buildQuery({ limit })}`, { signal }).then(
        (data) => data.events ?? [],
      ),
  });
}

export function useSystem() {
  return useQuery({
    queryKey: queryKeys.system(),
    queryFn: ({ signal }) => apiFetch<SystemResponse>('system', { signal }),
  });
}

// ---------------------------------------------------------------------------
// Phase 2: intelligence reads
// ---------------------------------------------------------------------------

export function useScores() {
  return useQuery({
    queryKey: queryKeys.scores(),
    queryFn: ({ signal }) =>
      apiFetch<{ providers: ProviderScore[] | null; models: ModelScore[] | null }>('scores', {
        signal,
      }),
    refetchInterval: 30000,
  });
}

export interface CacheStatsResponse {
  enabled: boolean;
  stats: CacheStats;
  config?: Record<string, unknown>;
}

export function useCacheStats(tenantId = '') {
  return useQuery({
    queryKey: queryKeys.cacheStats(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<CacheStatsResponse>(
        `cache/stats${buildQuery({ tenant_id: tenantId || undefined })}`,
        { signal },
      ),
    refetchInterval: 15000,
  });
}

export function useCachePolicies(tenantId = '') {
  return useQuery({
    queryKey: queryKeys.cachePolicies(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<{ policies: CachePolicy[] | null }>(
        `cache/policies${buildQuery({ tenant_id: tenantId || undefined })}`,
        { signal },
      ).then((d) => d.policies ?? []),
  });
}

export interface CacheInspectFilters {
  tenantId?: string;
  model?: string;
  provider?: string;
  limit?: number;
}

export function useCacheInspect(filters: CacheInspectFilters) {
  const params = {
    tenant_id: filters.tenantId,
    model: filters.model,
    provider: filters.provider,
    limit: filters.limit ?? 25,
  };
  return useQuery({
    queryKey: queryKeys.cacheInspect(params),
    queryFn: ({ signal }) =>
      apiFetch<{ entries: CacheEntry[] | null }>(`cache/inspect${buildQuery(params)}`, {
        signal,
      }).then((d) => d.entries ?? []),
  });
}

export function useCacheInvalidations(tenantId = '') {
  return useQuery({
    queryKey: queryKeys.cacheInvalidations(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<{ events: CacheInvalidation[] | null }>(
        `cache/invalidations${buildQuery({ tenant_id: tenantId || undefined, limit: 20 })}`,
        { signal },
      ).then((d) => d.events ?? []),
  });
}

export function useReplayJobs(tenantId: string) {
  return useQuery({
    queryKey: queryKeys.replay(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<{ jobs: ReplayJob[] | null }>(`replay${buildQuery({ tenant_id: tenantId })}`, {
        signal,
      }).then((d) => d.jobs ?? []),
    // Jobs run asynchronously in the gateway; poll so queued → running →
    // completed transitions appear without a manual refresh.
    refetchInterval: 5_000,
  });
}

export function useEvalRuns(tenantId: string) {
  return useQuery({
    queryKey: queryKeys.evals(tenantId),
    queryFn: ({ signal }) =>
      apiFetch<{ runs: EvaluationRun[] | null }>(
        `evaluations${buildQuery({ tenant_id: tenantId })}`,
        { signal },
      ).then((d) => d.runs ?? []),
  });
}

export function useEvalDetail(id: string) {
  return useQuery({
    queryKey: queryKeys.evalDetail(id),
    queryFn: ({ signal }) =>
      apiFetch<{ run: EvaluationRun; results: EvaluationResult[] | null }>(
        `evaluations/${encodeURIComponent(id)}`,
        { signal },
      ),
    enabled: id.length > 0,
  });
}

export function useEndpoints() {
  return useQuery({
    queryKey: queryKeys.endpoints(),
    queryFn: ({ signal }) =>
      apiFetch<{ endpoints: Endpoint[] | null }>('endpoints', { signal }).then(
        (d) => d.endpoints ?? [],
      ),
  });
}

export function useExplain(requestId: string) {
  return useQuery({
    queryKey: queryKeys.explain(requestId),
    queryFn: ({ signal }) =>
      apiFetch<RoutingExplanation>(`requests/${encodeURIComponent(requestId)}/explain`, {
        signal,
      }),
    enabled: requestId.length > 0,
  });
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

/**
 * Run an on-demand health probe against one provider.
 *
 * The response is the fresh health assessment, which is written straight into
 * the cache so the row updates without waiting for a refetch.
 */
export function useProbeProvider() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (providerId: string) =>
      apiFetch<ProviderHealth>(`providers/${encodeURIComponent(providerId)}/probe`, { method: 'POST' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.providerHealth() });
    },
  });
}

/** Force the gateway to re-read its policy cache. */
export function useReloadPolicies() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<{ reloaded: number }>('policies/reload', { method: 'POST' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.policies() });
    },
  });
}

export interface CreateKeyInput {
  tenantId: string;
  name: string;
  scopes?: string[];
  expiresInHours?: number;
}

export interface CreateKeyResult {
  key: APIKey;
  /** The plaintext credential, returned by the gateway exactly once. */
  plaintext: string;
  warning?: string;
}

export function useCreateKey() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateKeyInput) =>
      apiFetch<CreateKeyResult>('keys', {
        method: 'POST',
        body: {
          tenant_id: input.tenantId,
          name: input.name,
          scopes: input.scopes,
          expires_in_hours: input.expiresInHours,
        },
      }),
    onSuccess: (_result, variables) => {
      void client.invalidateQueries({ queryKey: queryKeys.keys(variables.tenantId) });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useRevokeKey() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; tenantId: string }) =>
      apiFetch<{ revoked: string }>(`keys/${encodeURIComponent(input.id)}`, { method: 'DELETE' }),
    onSuccess: (_result, variables) => {
      void client.invalidateQueries({ queryKey: queryKeys.keys(variables.tenantId) });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// ---------------------------------------------------------------------------
// Phase 2: writes
// ---------------------------------------------------------------------------

export function useKillProvider() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; kill: boolean; reason?: string }) =>
      apiFetch<{ provider: string; killed: boolean }>(`providers/${encodeURIComponent(input.id)}/kill`, {
        method: 'POST',
        body: { kill: input.kill, reason: input.reason },
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
    },
  });
}

export interface FlushCacheInput {
  tenantId?: string;
  scope?: string;
  model?: string;
  provider?: string;
  key?: string;
  reason?: string;
}

export function useInvalidateCache() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: string | FlushCacheInput) => {
      const body =
        typeof input === 'string'
          ? { tenant_id: input }
          : {
              tenant_id: input.tenantId ?? '',
              scope: input.scope,
              model: input.model,
              provider: input.provider,
              key: input.key,
              reason: input.reason,
            };
      return apiFetch<{ invalidated: number; scope?: string }>('cache/invalidate', {
        method: 'POST',
        body,
      });
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['cache-stats'] });
      void client.invalidateQueries({ queryKey: ['cache-inspect'] });
      void client.invalidateQueries({ queryKey: ['cache-invalidations'] });
    },
  });
}

// ---------------------------------------------------------------------------
// Temporary public tunnels
// ---------------------------------------------------------------------------

export function useTunnelStatus() {
  return useQuery({
    queryKey: ['tunnel-status'],
    queryFn: ({ signal }) => apiFetch<TunnelStatusResponse>('tunnels/status', { signal }),
    // Poll while mounted: creation resolves the URL asynchronously and the
    // supervisor can restart the process at any time.
    refetchInterval: 5000,
  });
}

export function useTunnelHistory(limit = 20) {
  return useQuery({
    queryKey: ['tunnel-history', limit],
    queryFn: ({ signal }) =>
      apiFetch<{ sessions: TunnelSession[] | null }>(`tunnels/history${buildQuery({ limit })}`, {
        signal,
      }).then((d) => d.sessions ?? []),
  });
}

function invalidateTunnel(client: ReturnType<typeof useQueryClient>) {
  void client.invalidateQueries({ queryKey: ['tunnel-status'] });
  void client.invalidateQueries({ queryKey: ['tunnel-history'] });
}

export function useCreateTunnel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { target?: string }) =>
      apiFetch<TunnelSession>('tunnels/create', { method: 'POST', body: input }),
    onSuccess: () => invalidateTunnel(client),
  });
}

export function useStopTunnel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id?: string }) =>
      apiFetch<TunnelSession>('tunnels/stop', { method: 'POST', body: input }),
    onSuccess: () => invalidateTunnel(client),
  });
}

export function useRestartTunnel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { target?: string }) =>
      apiFetch<TunnelSession>('tunnels/restart', { method: 'POST', body: input }),
    onSuccess: () => invalidateTunnel(client),
  });
}

export function useUpsertCachePolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CachePolicyInput) =>
      apiFetch<CachePolicy>('cache/policies', { method: 'PUT', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['cache-policies'] });
      void client.invalidateQueries({ queryKey: ['cache-stats'] });
    },
  });
}

export function useDeleteCachePolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string }>(`cache/policies/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['cache-policies'] });
    },
  });
}

export function useCreateReplay() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { name?: string; request_ids: string[]; providers?: string[]; models?: string[]; tenant_id?: string }) =>
      apiFetch<ReplayJob>('replay', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['replay'] });
    },
  });
}

export function useUpsertEndpoint() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: Partial<Endpoint> & { slug: string }) =>
      apiFetch<Endpoint>('endpoints', { method: 'PUT', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.endpoints() });
    },
  });
}

export function useUpdateBudget() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { tenant_id: string; period: string; limit_usd: number; enforced: boolean; hard_cap_usd?: number }) =>
      apiFetch<unknown>('budgets', { method: 'PUT', body: input }),
    onSuccess: (_r, v) => {
      void client.invalidateQueries({ queryKey: queryKeys.budgets(v.tenant_id) });
    },
  });
}

// ---------------------------------------------------------------------------
// Phase 3: full management writes
// ---------------------------------------------------------------------------

function invalidateAdminCaches(client: ReturnType<typeof useQueryClient>) {
  void client.invalidateQueries({ queryKey: queryKeys.providers() });
  void client.invalidateQueries({ queryKey: queryKeys.models() });
  void client.invalidateQueries({ queryKey: queryKeys.tenants() });
  void client.invalidateQueries({ queryKey: queryKeys.policies() });
  void client.invalidateQueries({ queryKey: queryKeys.endpoints() });
  void client.invalidateQueries({ queryKey: ['overrides'] });
  void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
}

// --- Providers ---

export function useCreateProvider() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: ProviderInput) => apiFetch<Provider>('providers', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useUpdateProvider() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; body: Record<string, unknown> }) =>
      apiFetch<Provider>(`providers/${encodeURIComponent(input.id)}`, { method: 'PUT', body: input.body }),
    onSuccess: () => {
      invalidateAdminCaches(client);
    },
  });
}

export function usePatchProvider() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; body: Record<string, unknown> }) =>
      apiFetch<Provider>(`providers/${encodeURIComponent(input.id)}`, { method: 'PATCH', body: input.body }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeleteProvider() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string; models_removed: number }>(`providers/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export interface SetCredentialResponse {
  provider_id: string;
  has_credential: boolean;
  credential?: CredentialMeta;
  sync?: ProviderSyncResponse;
  sync_error?: string;
}

export function useSetCredential() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { providerId: string; secret: string; name?: string; syncModels?: boolean }) =>
      apiFetch<SetCredentialResponse>(`providers/${encodeURIComponent(input.providerId)}/credential`, {
        method: 'PUT',
        body: { secret: input.secret, name: input.name, sync_models: input.syncModels },
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeleteCredential() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (providerId: string) =>
      apiFetch<{ provider_id: string; has_credential: boolean }>(
        `providers/${encodeURIComponent(providerId)}/credential`,
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export interface TestProviderInput {
  providerId: string;
  checks?: Array<'connectivity' | 'models' | 'sample'>;
  model?: string;
  prompt?: string;
}

export function useTestProvider() {
  return useMutation({
    mutationFn: (input: TestProviderInput) =>
      apiFetch<ProviderTestResponse>(`providers/${encodeURIComponent(input.providerId)}/test`, {
        method: 'POST',
        body: {
          checks: input.checks,
          model: input.model,
          prompt: input.prompt,
        },
      }),
  });
}

export interface ProviderSyncResponse {
  provider_id: string;
  created: string[] | null;
  skipped: string[] | null;
  total: number;
}

/** Discover a provider's remote models and populate the registry with them. */
export function useSyncModels() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (providerId: string) =>
      apiFetch<ProviderSyncResponse>(`providers/${encodeURIComponent(providerId)}/sync-models`, {
        method: 'POST',
        body: {},
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.providers() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useProviderTests(providerId: string, limit = 10) {
  return useQuery({
    queryKey: queryKeys.providerTests(providerId, limit),
    queryFn: ({ signal }) =>
      apiFetch<{ results: ProviderTestResult[] | null }>(
        `providers/${encodeURIComponent(providerId)}/tests${buildQuery({ limit })}`,
        { signal },
      ).then((data) => data.results ?? []),
    enabled: providerId.length > 0,
  });
}

// --- Models ---

export function useCreateModel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: ModelInput) => apiFetch<Model>('models', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useUpdateModel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; body: Record<string, unknown> }) =>
      apiFetch<Model>(`models/${encodeURIComponent(input.id)}`, { method: 'PUT', body: input.body }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function usePatchModel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; body: Record<string, unknown> }) =>
      apiFetch<Model>(`models/${encodeURIComponent(input.id)}`, { method: 'PATCH', body: input.body }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeleteModel() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string }>(`models/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.models() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Tenants ---

export function useCreateTenant() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: TenantInput) => apiFetch<Tenant>('tenants', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tenants() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useUpdateTenant() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; body: Record<string, unknown> }) =>
      apiFetch<Tenant>(`tenants/${encodeURIComponent(input.id)}`, { method: 'PUT', body: input.body }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tenants() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeleteTenant() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; force?: boolean }) =>
      apiFetch<{ deleted: string }>(
        `tenants/${encodeURIComponent(input.id)}${input.force ? '?force=true' : ''}`,
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tenants() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Keys ---

export function useUpdateKey() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; tenantId: string; body: KeyUpdateInput }) =>
      apiFetch<APIKey>(`keys/${encodeURIComponent(input.id)}`, { method: 'PATCH', body: input.body }),
    onSuccess: (_result, variables) => {
      void client.invalidateQueries({ queryKey: queryKeys.keys(variables.tenantId) });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export interface RotateKeyResult {
  key: APIKey;
  plaintext: string;
  warning?: string;
}

export function useRotateKey() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; tenantId: string }) =>
      apiFetch<RotateKeyResult>(`keys/${encodeURIComponent(input.id)}/rotate`, { method: 'POST' }),
    onSuccess: (_result, variables) => {
      void client.invalidateQueries({ queryKey: queryKeys.keys(variables.tenantId) });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Policies ---

export function useCreatePolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: PolicyInput) => apiFetch<RoutingPolicy>('policies', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.policies() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useUpsertPolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: PolicyInput) => apiFetch<RoutingPolicy>('policies', { method: 'PUT', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.policies() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeletePolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string }>(`policies/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.policies() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Overrides ---

export function useOverrides(limit = 100) {
  return useQuery({
    queryKey: queryKeys.overrides(limit),
    queryFn: ({ signal }) =>
      apiFetch<{ overrides: Override[] | null }>(`overrides${buildQuery({ limit })}`, { signal }).then(
        (data) => data.overrides ?? [],
      ),
  });
}

export function useCreateOverride() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: OverrideInput) => apiFetch<Override>('overrides', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['overrides'] });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Endpoints ---

export function useDeleteEndpoint() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string }>(`endpoints/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.endpoints() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// ---------------------------------------------------------------------------
// Phase 4: tools, policies, invocations and agent runs.
// ---------------------------------------------------------------------------

export function useTools() {
  return useQuery({
    queryKey: queryKeys.tools(),
    queryFn: ({ signal }) =>
      apiFetch<{ tools: Tool[] | null }>('tools', { signal }).then((data) => data.tools ?? []),
  });
}

export function useCreateTool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: ToolInput) => apiFetch<Tool>('tools', { method: 'POST', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tools() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useUpdateTool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; patch: Partial<ToolInput> }) =>
      apiFetch<Tool>(`tools/${encodeURIComponent(input.id)}`, { method: 'PUT', body: input.patch }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tools() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useSetToolEnabled() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; enabled: boolean }) =>
      apiFetch<{ id: string; enabled: boolean }>(`tools/${encodeURIComponent(input.id)}/enabled`, {
        method: 'POST',
        body: { enabled: input.enabled },
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tools() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeleteTool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string }>(`tools/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.tools() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Tool policies ---

export function useToolPolicies() {
  return useQuery({
    queryKey: queryKeys.toolPolicies(),
    queryFn: ({ signal }) =>
      apiFetch<{ policies: ToolPolicy[] | null }>('tool-policies', { signal }).then(
        (data) => data.policies ?? [],
      ),
  });
}

export function useUpsertToolPolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: ToolPolicyInput) =>
      apiFetch<ToolPolicy>('tool-policies', { method: 'PUT', body: input }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.toolPolicies() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

export function useDeleteToolPolicy() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ deleted: string }>(`tool-policies/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.toolPolicies() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

// --- Invocations and runs ---

export function useToolInvocations(limit = 100) {
  return useQuery({
    queryKey: queryKeys.toolInvocations(limit),
    queryFn: ({ signal }) =>
      apiFetch<{ invocations: ToolInvocation[] | null; total: number }>(
        `tool-invocations${buildQuery({ limit })}`,
        { signal },
      ),
  });
}

export function useToolInvocationDetail(id: string | null) {
  return useQuery({
    queryKey: queryKeys.toolInvocationDetail(id ?? ''),
    enabled: id != null && id !== '',
    queryFn: ({ signal }) =>
      apiFetch<{ invocation: ToolInvocation }>(`tool-invocations/${encodeURIComponent(id ?? '')}`, {
        signal,
      }),
  });
}

export function useAgentRuns(limit = 100) {
  return useQuery({
    queryKey: queryKeys.agentRuns(limit),
    queryFn: ({ signal }) =>
      apiFetch<{ runs: AgentRun[] | null; total: number }>(
        `agent-runs${buildQuery({ limit })}`,
        { signal },
      ),
  });
}

export function useAgentRunDetail(id: string | null) {
  return useQuery({
    queryKey: queryKeys.agentRunDetail(id ?? ''),
    enabled: id != null && id !== '',
    queryFn: ({ signal }) =>
      apiFetch<{ run: AgentRun; steps: AgentStep[] }>(`agent-runs/${encodeURIComponent(id ?? '')}`, {
        signal,
      }),
  });
}

/**
 * The signed-in operator.
 *
 * Used by the sidebar to state who is signed in. It reads the same `/auth/me`
 * endpoint the server used to authorise the page, so the name in the rail is the
 * name the gateway authenticated rather than something cached in the browser.
 */
export interface CurrentUser {
  username: string;
  created_at: string;
  last_login_at?: string;
}

export function useCurrentUser() {
  return useQuery({
    // Stable key: the identity does not change within a session, and a refetch on
    // every rail render would be a request per navigation.
    queryKey: ['dashboard-auth', 'me'] as const,
    queryFn: ({ signal }) =>
      apiFetch<CurrentUser>('auth/me', { signal }).catch(() => null),
    // A missing session is a normal state, not an error worth retrying.
    staleTime: 60_000,
    retry: false,
  });
}

// ---------------------------------------------------------------------------
// Cost intelligence
// ---------------------------------------------------------------------------

export interface CostFilters {
  tenantId: string;
  /** A Go duration such as "24h"; see windowToFromParam. */
  from: string;
  provider?: string;
  model?: string;
  endpointId?: string;
}

function costQuery(filters: CostFilters) {
  return {
    tenant_id: filters.tenantId,
    from: filters.from,
    provider: filters.provider,
    model: filters.model,
    endpoint_id: filters.endpointId,
  };
}

/** Window totals: actual vs estimated spend, accuracy, unit rate, savings. */
export function useCostOverview(filters: CostFilters) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'overview', ...filters }),
    queryFn: ({ signal }) =>
      apiFetch<CostOverviewResponse>(`cost/overview${buildQuery(costQuery(filters))}`, { signal }),
  });
}

/** Spend grouped by one dimension: provider | model | tenant | endpoint | request_type. */
export function useCostGrouped(filters: CostFilters, dimension: string) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'by', dimension, ...filters }),
    queryFn: ({ signal }) =>
      apiFetch<CostGroupedResponse>(
        `cost/by${buildQuery({ ...costQuery(filters), dimension })}`,
        { signal },
      ).then((data) => ({ ...data, rows: data.rows ?? [] })),
  });
}

/** Actual vs estimated spend over time for the trend chart. */
export function useCostSeries(filters: CostFilters) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'series', ...filters }),
    queryFn: ({ signal }) =>
      apiFetch<CostSeriesResponse>(`cost/series${buildQuery(costQuery(filters))}`, { signal }),
  });
}

/** The window's most expensive requests, with breakdowns attached. */
export function useCostTop(filters: CostFilters, limit = 10) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'top', limit, ...filters }),
    queryFn: ({ signal }) =>
      apiFetch<CostTopResponse>(`cost/requests/top${buildQuery({ ...costQuery(filters), limit })}`, {
        signal,
      }),
  });
}

/** One request's exact cost trace. */
export function useCostRequest(requestId: string | null) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'request', requestId: requestId ?? '' }),
    enabled: requestId != null && requestId !== '',
    queryFn: ({ signal }) =>
      apiFetch<CostRequestResponse>(`cost/requests/${encodeURIComponent(requestId ?? '')}`, {
        signal,
      }),
  });
}

/** Versioned price sheets, newest first. */
export function usePricing() {
  return useQuery({
    queryKey: queryKeys.pricing(),
    queryFn: ({ signal }) =>
      apiFetch<PricingListResponse>('cost/pricing', { signal }).then((data) => data.pricing ?? []),
  });
}

export interface CreatePricingInput {
  scope: string;
  scopeId?: string;
  currency?: string;
  inputCostPerMillion: number;
  outputCostPerMillion: number;
  cachedInputCostPerMillion?: number;
  baseFeeUsd?: number;
  effectiveFrom?: string;
}

/** Mint one immutable price sheet. There is no update: a price change is a new row. */
export function useCreatePricing() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreatePricingInput) =>
      apiFetch<{ pricing: PricingVersion }>('cost/pricing', {
        method: 'POST',
        body: {
          scope: input.scope,
          scope_id: input.scopeId,
          currency: input.currency,
          input_cost_per_million: input.inputCostPerMillion,
          output_cost_per_million: input.outputCostPerMillion,
          cached_input_cost_per_million: input.cachedInputCostPerMillion,
          base_fee_usd: input.baseFeeUsd,
          effective_from: input.effectiveFrom,
        },
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.pricing() });
      void client.invalidateQueries({ queryKey: queryKeys.audit(100) });
    },
  });
}

/** Budgets evaluated against authoritative spend, plus recently fired alerts. */
export function useCostBudgets(tenantId: string) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'budgets', tenantId }),
    queryFn: ({ signal }) =>
      apiFetch<CostBudgetsResponse>(`cost/budgets${buildQuery({ tenant_id: tenantId })}`, {
        signal,
      }),
  });
}

/** Open spend anomalies. Reading detects fresh spikes as a side effect. */
export function useCostAnomalies(tenantId: string) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'anomalies', tenantId }),
    queryFn: ({ signal }) =>
      apiFetch<CostAnomaliesResponse>(`cost/anomalies${buildQuery({ tenant_id: tenantId })}`, {
        signal,
      }).then((data) => ({ anomalies: data.anomalies ?? [] })),
  });
}

export function useResolveAnomaly() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ resolved: string }>(`cost/anomalies/${encodeURIComponent(id)}/resolve`, {
        method: 'POST',
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['cost'] });
    },
  });
}

/** Month-end projection from observed daily spend. */
export function useCostForecast(filters: CostFilters) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'forecast', ...filters }),
    queryFn: ({ signal }) =>
      apiFetch<CostForecastResponse>(`cost/forecast${buildQuery(costQuery(filters))}`, {
        signal,
      }),
  });
}

/** Measured avoided spend by source: cache, routing, fallback. */
export function useCostSavings(filters: CostFilters) {
  return useQuery({
    queryKey: queryKeys.cost({ view: 'savings', ...filters }),
    queryFn: ({ signal }) =>
      apiFetch<CostSavingsResponse>(`cost/savings${buildQuery(costQuery(filters))}`, { signal }),
  });
}
