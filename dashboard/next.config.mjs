/**
 * Next.js configuration.
 *
 * `output: 'standalone'` is what the production image depends on: it emits a
 * self-contained server bundle with only the modules actually imported, so the
 * runtime image does not need node_modules. See deploy/docker/Dockerfile.dashboard.
 *
 * `outputFileTracingRoot` pins tracing to this directory. Without it, a
 * checkout nested under another lockfile (like this repository) makes Next
 * infer the workspace root above the dashboard, which nests the standalone
 * output one level deeper and breaks every launcher that expects
 * `.next/standalone/server.js` — the container build, `astrarouter native up`,
 * and the systemd unit alike.
 *
 * @type {import('next').NextConfig}
 */
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const dashboardDir = path.dirname(fileURLToPath(import.meta.url));

const nextConfig = {
  output: 'standalone',
  outputFileTracingRoot: dashboardDir,
  reactStrictMode: true,
  // The framework version is not an interesting disclosure.
  poweredByHeader: false,
  eslint: {
    // Linting is a separate concern from building; a lint rule should not be able
    // to fail a container image build.
    ignoreDuringBuilds: true,
  },
  async headers() {
    return [
      {
        source: '/:path*',
        headers: [
          // The dashboard renders operator-supplied values (model names, key
          // names, error text) into the DOM, so a strict content policy and
          // framing denial are appropriate even though the surface is internal.
          { key: 'X-Content-Type-Options', value: 'nosniff' },
          { key: 'X-Frame-Options', value: 'DENY' },
          { key: 'Referrer-Policy', value: 'no-referrer' },
        ],
      },
    ];
  },
};

export default nextConfig;
