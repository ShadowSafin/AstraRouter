/**
 * Console session, server-side only.
 *
 * The gateway owns authentication; this module is the dashboard's client for it.
 * Nothing here decides whether a session is valid — it asks the gateway, which is
 * the only component that can, and caches nothing locally that could outlive a
 * revocation.
 *
 * `server-only` is load-bearing. A cookie name and a fetch helper in the browser
 * bundle would be harmless, but the moment this file is imported from a client
 * component it becomes an invitation to do session validation in the browser,
 * which cannot enforce anything.
 */
import 'server-only';

import { cookies } from 'next/headers';

import { GATEWAY_URL } from '@/lib/gateway';

/** Cookie name. Must match `admin.dashboard_auth.cookie_name` on the gateway. */
export const SESSION_COOKIE = process.env.ASTRAROUTER_SESSION_COOKIE ?? 'astrarouter_session';

export interface SessionUser {
  username: string;
  created_at: string;
  last_login_at?: string;
}

export interface AuthState {
  setup_required: boolean;
  authenticated: boolean;
  username?: string;
  policy: {
    min_password_length: number;
    max_password_length: number;
    max_failed_attempts: number;
    session_minutes: number;
  };
}

/**
 * Read the session cookie value.
 *
 * Returned raw for forwarding to the gateway, which is what validates it. The
 * dashboard never interprets the token: doing so would require a signing secret
 * in the dashboard and create a second source of truth for "is this signed in".
 */
export async function readSessionToken(): Promise<string> {
  const jar = await cookies();
  return jar.get(SESSION_COOKIE)?.value ?? '';
}

/**
 * Resolve the current operator, or null when there is no valid session.
 *
 * The gateway reads the session cookie, so the cookie is forwarded as a Cookie
 * header rather than as a bearer token. Inventing a second accepted credential
 * format in the gateway for the dashboard's convenience would mean two paths
 * guarding one thing.
 *
 * A transport failure returns null rather than throwing: the caller's correct
 * response to "we cannot tell" is to require login, not to render a broken page.
 */
export async function currentUser(): Promise<SessionUser | null> {
  const jar = await cookies();
  const token = jar.get(SESSION_COOKIE)?.value;
  if (!token) return null;

  try {
    const response = await fetch(`${GATEWAY_URL}/admin/v1/auth/me`, {
      headers: { cookie: `${SESSION_COOKIE}=${token}`, accept: 'application/json' },
      cache: 'no-store',
    });
    if (!response.ok) return null;
    const body = (await response.json()) as SessionUser;
    return typeof body?.username === 'string' ? body : null;
  } catch {
    return null;
  }
}

/** Read the gateway's auth state, including whether setup is still required. */
export async function authState(): Promise<AuthState | null> {
  try {
    const response = await fetch(`${GATEWAY_URL}/admin/v1/auth/state`, {
      headers: { accept: 'application/json' },
      cache: 'no-store',
    });
    if (!response.ok) return null;
    return (await response.json()) as AuthState;
  } catch {
    return null;
  }
}

/**
 * True when the gateway has dashboard auth mounted.
 *
 * When it is absent the dashboard must not present a login form that can never
 * succeed; it falls back to the previous behaviour so a gateway deployed without
 * the feature stays usable.
 */
export async function dashboardAuthEnabled(): Promise<boolean> {
  return (await authState()) !== null;
}
