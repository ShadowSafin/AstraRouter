import { NextResponse } from 'next/server';

import { GATEWAY_URL } from '@/lib/gateway';

/**
 * Public client configuration.
 *
 * The gateway base URLs are not secrets (clients must know them to call the
 * API), so they are safe to expose to the browser. GATEWAY_LAN_URL is the
 * operator-configured LAN address (e.g. http://192.168.1.20:18080) shown
 * alongside localhost so anyone on the network can use an endpoint. The admin
 * key never leaves the server: only URLs are returned.
 */
export async function GET() {
  return NextResponse.json({
    gateway_url: GATEWAY_URL,
    lan_url: process.env.GATEWAY_LAN_URL ?? '',
  });
}
