/**
 * Browser-side admin API client.
 *
 * Requests go to the same-origin proxy (`/api/gateway/...`), which attaches the
 * administrative credential server-side. Nothing here knows the credential
 * exists, which is exactly the point.
 */
import type { ErrorEnvelope } from './types';

/** Base path of the same-origin proxy. */
const PROXY_BASE = '/api/gateway';

/** An API failure carrying the gateway's own message and HTTP status. */
export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly type?: string;

  constructor(message: string, status: number, code?: string, type?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.type = type;
  }

  /** True when the dashboard is misconfigured rather than the request wrong. */
  get isConfigurationError(): boolean {
    return this.code === 'dashboard_missing_admin_key' || this.code === 'dashboard_upstream_unreachable';
  }

  get isUnauthorized(): boolean {
    return this.status === 401 || this.status === 403;
  }
}

function describeStatus(status: number): string {
  switch (status) {
    case 401:
      return 'the gateway rejected the dashboard credential (401)';
    case 403:
      return 'the dashboard credential lacks the required scope (403)';
    case 404:
      return 'not found';
    case 503:
      return 'a backing service is unavailable (503)';
    default:
      return `the request failed with status ${status}`;
  }
}

async function parseError(response: Response): Promise<ApiError> {
  const fallback = describeStatus(response.status);
  try {
    const text = await response.text();
    if (!text) return new ApiError(fallback, response.status);
    const parsed = JSON.parse(text) as Partial<ErrorEnvelope> & { message?: string };
    const envelope = parsed.error;
    if (envelope?.message) {
      return new ApiError(envelope.message, response.status, envelope.code, envelope.type);
    }
    if (typeof parsed.message === 'string') {
      return new ApiError(parsed.message, response.status);
    }
    return new ApiError(fallback, response.status);
  } catch {
    return new ApiError(fallback, response.status);
  }
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  /** Serialized as JSON with the right content type. */
  body?: unknown;
  signal?: AbortSignal;
}

/**
 * Perform an admin API request and decode the JSON response.
 *
 * Throws ApiError on any non-2xx, so callers can rely on the resolved value
 * being the payload. React Query surfaces the thrown error as `error`, which is
 * what the shared error state renders.
 */
export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET';

  const init: RequestInit = {
    method,
    headers: { accept: 'application/json' },
    credentials: 'same-origin',
    signal: options.signal,
  };
  if (options.body !== undefined) {
    init.headers = { ...(init.headers as Record<string, string>), 'content-type': 'application/json' };
    init.body = JSON.stringify(options.body);
  }

  let response: Response;
  try {
    response = await fetch(`${PROXY_BASE}/${path.replace(/^\/+/, '')}`, init);
  } catch (cause) {
    // A network-level failure is almost always the Next server being down, which
    // is a different problem from the gateway being down.
    throw new ApiError(
      cause instanceof Error ? cause.message : 'the dashboard server is unreachable',
      0,
      'dashboard_offline',
    );
  }

  if (!response.ok) {
    throw await parseError(response);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

/** Build a query string, dropping empty values so the gateway sees clean input. */
export function buildQuery(params: Record<string, string | number | undefined | null>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue;
    search.set(key, String(value));
  }
  const rendered = search.toString();
  return rendered ? `?${rendered}` : '';
}

/** Serialize a request-log list as CSV for export. */
export function toCsv(rows: Array<Record<string, unknown>>, columns: string[]): string {
  const escape = (value: unknown): string => {
    const text = value === null || value === undefined ? '' : String(value);
    if (/[",\n]/.test(text)) {
      return `"${text.replace(/"/g, '""')}"`;
    }
    return text;
  };
  const header = columns.join(',');
  const body = rows.map((row) => columns.map((column) => escape(row[column])).join(','));
  return [header, ...body].join('\n');
}
