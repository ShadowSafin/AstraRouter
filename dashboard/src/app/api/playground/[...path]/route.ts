/**
 * Server-side proxy from the Playground to the public inference API.
 *
 * Why a proxy rather than calling the gateway from the browser:
 *
 *   1. The gateway the browser can reach is not necessarily the one the server
 *      can reach, and the API URL is baked into the bundle at build time. The
 *      server already knows where the gateway is.
 *   2. The operator's API key stays out of URLs and out of the gateway's CORS
 *      surface: it is sent as a header, for one request, and never stored.
 *   3. Streaming needs no CORS preflight negotiation.
 *
 * The route is gated on the operator session exactly like the admin proxy. That
 * matters: without the check this would be an open, unauthenticated inference
 * endpoint that only requires an API key the caller already supplies — but it
 * would also let anyone reach the gateway through the dashboard's origin.
 *
 * The target path is a hard whitelist, so this is not a general gateway proxy.
 */
import { NextResponse } from 'next/server';

import { GATEWAY_URL, verifySession } from '@/lib/gateway';

export const dynamic = 'force-dynamic';
export const revalidate = 0;

/** The only inference surfaces the Playground is allowed to reach. */
const ALLOWED_ROUTES = new Set(['v1/chat/completions']);

/**
 * Routing-intent headers forwarded to the gateway, verbatim.
 *
 * This is the complete set the gateway reads (see documentation/api.md), and
 * nothing more. In particular there is no provider or tenant header to forward:
 * the provider is chosen by routing and reported back, and the tenant comes from
 * the API key. Forwarding a header the gateway ignores would let the console
 * imply control it does not have.
 */
const INTENT_HEADERS = [
  'x-synapass-debug',
  'x-synapass-endpoint',
  'x-synapass-policy',
  'x-synapass-no-fallback',
  'x-synapass-no-cache',
  'x-synapass-region',
  'x-synapass-sensitivity',
  'x-synapass-max-cost-usd',
  'x-synapass-latency-target-ms',
];

interface RouteContext {
  params: Promise<{ path: string[] }>;
}

export async function POST(request: Request, context: RouteContext): Promise<Response> {
  const { path } = await context.params;
  const route = path.join('/');

  if (!ALLOWED_ROUTES.has(route)) {
    return NextResponse.json(
      {
        error: {
          message: `the playground does not target ${route}`,
          type: 'invalid_request_error',
          code: 'playground_unsupported_route',
        },
      },
      { status: 404 },
    );
  }

  const cookie = request.headers.get('cookie') ?? '';
  if (!(await verifySession(cookie))) {
    return NextResponse.json(
      {
        error: {
          message: 'authentication is required',
          type: 'authentication_error',
          code: 'not_authenticated',
        },
      },
      { status: 401 },
    );
  }

  // The credential is supplied per session by the operator. It is read from a
  // header, used once, and never written anywhere — not to a log, not to disk.
  const apiKey = request.headers.get('x-synapass-key')?.trim() ?? '';
  if (!apiKey) {
    return NextResponse.json(
      {
        error: {
          message:
            'an API key is required for the run. The public inference API authenticates with a tenant API key; mint one on the API keys page or paste an existing one.',
          type: 'invalid_request_error',
          code: 'playground_missing_api_key',
        },
      },
      { status: 400 },
    );
  }

  const body = await request.text();

  const headers = new Headers({
    accept: request.headers.get('accept') ?? 'application/json',
    'content-type': request.headers.get('content-type') ?? 'application/json',
    authorization: `Bearer ${apiKey}`,
  });
  for (const name of INTENT_HEADERS) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }

  let upstream: Response;
  try {
    upstream = await fetch(`${GATEWAY_URL}/${route}`, {
      method: 'POST',
      headers,
      body,
      cache: 'no-store',
      // Propagating the signal is what makes the UI's Cancel button actually
      // stop generation upstream rather than just abandoning the response.
      signal: request.signal,
    });
  } catch (cause) {
    return NextResponse.json(
      {
        error: {
          message:
            `the dashboard could not reach the gateway at ${GATEWAY_URL}. ` +
            'Check SYNAPASS_API_URL and that the gateway is running.',
          type: 'upstream_error',
          code: 'dashboard_upstream_unreachable',
          cause: cause instanceof Error ? cause.message : String(cause),
        },
      },
      { status: 502 },
    );
  }

  const outHeaders = new Headers({ 'cache-control': 'no-store' });
  const contentType = upstream.headers.get('content-type');
  if (contentType) outHeaders.set('content-type', contentType);
  // The correlation id is preserved so a run can be found in the request log
  // even if the operator loses the rendered metadata.
  const requestId = upstream.headers.get('x-request-id');
  if (requestId) outHeaders.set('x-request-id', requestId);

  // The body is streamed straight through: buffering it would turn a live token
  // stream into a stall followed by one large response.
  return new Response(upstream.body, { status: upstream.status, headers: outHeaders });
}
