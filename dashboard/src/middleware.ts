import { NextResponse, type NextRequest } from 'next/server';

/**
 * Route protection for the console.
 *
 * This is a redirect, not an access control. It keeps an unauthenticated visitor
 * off the application routes so they do not render a shell full of failed
 * requests, and it sends them to setup or login based on whether an operator
 * exists yet.
 *
 * The real gate is the admin API proxy, which refuses to attach the dashboard's
 * admin key unless the gateway confirms the session. Middleware runs in a
 * different runtime from the proxy and cannot reach Postgres, so a decision made
 * here is advisory by construction. Treating it as the boundary would be the
 * mistake this comment exists to prevent.
 */
const SESSION_COOKIE = process.env.ASTRAROUTER_SESSION_COOKIE ?? 'astrarouter_session';

/** Routes reachable without a session. */
const PUBLIC_PATHS = new Set(['/login', '/setup']);

/** Never redirect these: assets, framework internals and API routes. */
function isExcluded(pathname: string): boolean {
  if (
    pathname.startsWith('/_next') ||
    pathname.startsWith('/favicon') ||
    pathname.startsWith('/api') ||
    pathname === '/robots.txt'
  ) {
    return true;
  }
  // A file extension means a static asset rather than an application route.
  // Redirecting one to the login page does not protect anything, it breaks the
  // page that references it: an image on the auth screen would arrive as login
  // markup. Application routes here are extensionless, so this leaves them all
  // gated.
  return /\.[a-z0-9]+$/i.test(pathname);
}

export async function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl;

  if (isExcluded(pathname) || PUBLIC_PATHS.has(pathname)) {
    return NextResponse.next();
  }

  const hasCookie = Boolean(request.cookies.get(SESSION_COOKIE)?.value);
  if (!hasCookie) {
    // No cookie at all, so we know nothing about setup state and send the
    // operator to login. The login page redirects to setup when the gateway says
    // no operator exists, so this costs one extra hop at worst and never strands
    // a fresh installation on a form that cannot succeed.
    const url = request.nextUrl.clone();
    url.pathname = '/login';
    url.search = '';
    return NextResponse.redirect(url);
  }

  return NextResponse.next();
}

export const config = {
  // Everything except framework internals and static files.
  matcher: ['/((?!_next/static|_next/image|favicon.ico|robots.txt).*)'],
};
