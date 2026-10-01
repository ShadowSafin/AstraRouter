/**
 * Same-origin proxy to the gateway's admin API.
 *
 * Why a proxy rather than calling the gateway from the browser:
 *
 *   1. The admin API requires a credential. Shipping it in the browser bundle
 *      would make every visitor a platform operator.
 *   2. The gateway's admin routes are not CORS-enabled for the dashboard origin
 *      by default, and widening CORS to expose them would be worse.
 *   3. Inside docker compose the browser can reach the gateway on localhost but
 *      the dashboard server reaches it faster over the compose network.
 *
 * The route only ever targets `<gateway>/admin/v1/<path>`, so it cannot be
 * turned into a general-purpose forwarder for the gateway.
 */
import { NextResponse } from 'next/server';

import { hasAdminKey, proxyAdmin } from '@/lib/gateway';

export const dynamic = 'force-dynamic';
// Proxying is a pass-through, not a cache.
export const revalidate = 0;

interface RouteContext {
  params: Promise<{ path: string[] }>;
}

async function handle(request: Request, context: RouteContext): Promise<NextResponse> {
  const { path } = await context.params;

  if (!hasAdminKey()) {
    // Fail with an actionable message rather than a 401 from the gateway, which
    // reads as a credentials problem when it is a configuration one.
    return NextResponse.json(
      {
        error: {
          message:
            'the dashboard has no administrative credential configured. ' +
            'Set COREROUTER_ADMIN_KEY to the same value as the gateway\'s CR_ADMIN_KEY.',
          type: 'configuration_error',
          code: 'dashboard_missing_admin_key',
        },
      },
      { status: 503 },
    );
  }

  const search = new URL(request.url).search;
  const target = `${path.join('/')}${search}`;

  const method = request.method.toUpperCase();
  const body = method === 'GET' || method === 'HEAD' ? undefined : await request.text();

  const result = await proxyAdmin(method, target, {
    body,
    contentType: request.headers.get('content-type') ?? undefined,
    signal: request.signal,
  });

  return new NextResponse(result.body, {
    status: result.status,
    headers: {
      'content-type': result.contentType,
      'cache-control': 'no-store',
    },
  });
}

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const PATCH = handle;
export const DELETE = handle;
