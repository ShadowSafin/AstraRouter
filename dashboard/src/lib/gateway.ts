/**
 * Server-side gateway client.
 *
 * This module must never be imported from a client component: it reads the
 * administrative credential. `server-only` turns an accidental import into a
 * build error rather than a leaked secret, which is the only reliable way to
 * enforce that boundary in a codebase where both sides import from `@/lib`.
 */
import 'server-only';

/** The gateway as seen from the dashboard server. */
export const GATEWAY_URL = (process.env.SYNAPASS_API_URL ?? 'http://localhost:8080').replace(
  /\/+$/,
  '',
);

/**
 * The administrative credential. It is attached to every proxied request and
 * never reaches the browser: the whole reason the dashboard proxies rather than
 * calling the gateway directly.
 */
export const ADMIN_KEY = process.env.SYNAPASS_ADMIN_KEY ?? '';

export function hasAdminKey(): boolean {
  return ADMIN_KEY.trim().length > 0;
}

/**
 * Ask the gateway whether the session behind a Cookie header is real.
 *
 * Any non-200 means "not authenticated". There is deliberately no partial
 * trust: a proxy that guessed would be an open administrative backdoor. An
 * unreachable gateway also fails closed, so a gateway restart cannot be used to
 * slip past the check.
 *
 * Shared by every server-side proxy so there is exactly one definition of
 * "signed in", and a change to it cannot silently apply to one route only.
 */
export async function verifySession(cookie: string): Promise<boolean> {
  if (!cookie) return false;
  try {
    const response = await fetch(`${GATEWAY_URL}/admin/v1/auth/me`, {
      headers: { cookie, accept: 'application/json' },
      cache: 'no-store',
    });
    return response.ok;
  } catch {
    return false;
  }
}

export interface ProxyResult {
  status: number;
  contentType: string;
  body: string;
  /**
   * The upstream Set-Cookie header, verbatim, or empty.
   *
   * It is forwarded rather than reconstructed because the gateway owns the
   * cookie policy: httpOnly, Secure, SameSite and Path all have to match the
   * cookie it will later revoke.
   */
  setCookie: string;
}

/**
 * Forward one request to the gateway's admin API.
 *
 * The target path is constrained to `/admin/v1/...` by construction (the route
 * handler only receives the wildcard segment), so this cannot be used as an
 * open proxy to arbitrary gateway paths -- which matters because the admin key
 * would otherwise be attachable to any endpoint.
 */
export async function proxyAdmin(
  method: string,
  path: string,
  options: {
    body?: string;
    contentType?: string;
    signal?: AbortSignal;
    cookie?: string;
  } = {},
): Promise<ProxyResult> {
  const url = `${GATEWAY_URL}/admin/v1/${path.replace(/^\/+/, '')}`;

  const headers = new Headers({ accept: 'application/json' });
  if (ADMIN_KEY.trim()) {
    headers.set('authorization', `Bearer ${ADMIN_KEY.trim()}`);
  }
  // The operator's own Cookie header, forwarded verbatim. The gateway reads the
  // session cookie itself, so presenting it unchanged keeps exactly one accepted
  // way to authenticate a console operator.
  if (options.cookie?.trim()) {
    headers.set('cookie', options.cookie);
  }
  if (options.contentType) {
    headers.set('content-type', options.contentType);
  }

  let response: Response;
  try {
    response = await fetch(url, {
      method,
      headers,
      body: options.body,
      // Admin reads must reflect the moment they are requested; a cached usage
      // figure is worse than no figure.
      cache: 'no-store',
      redirect: 'manual',
      signal: options.signal,
    });
  } catch (cause) {
    // A transport failure is the dashboard's problem, not the gateway's, so it
    // is reported as a 502 with the same JSON envelope the client already parses.
    return {
      status: 502,
      contentType: 'application/json',
      setCookie: '',
      body: JSON.stringify({
        error: {
          message:
            `the dashboard could not reach the gateway at ${GATEWAY_URL}. ` +
            'Check SYNAPASS_API_URL and that the gateway is running.',
          type: 'upstream_error',
          code: 'dashboard_upstream_unreachable',
          cause: cause instanceof Error ? cause.message : String(cause),
        },
      }),
    };
  }

  return {
    status: response.status,
    contentType: response.headers.get('content-type') ?? 'application/json',
    setCookie: response.headers.get('set-cookie') ?? '',
    body: await response.text(),
  };
}
