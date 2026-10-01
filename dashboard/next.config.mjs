/**
 * Next.js configuration.
 *
 * `output: 'standalone'` is what the production image depends on: it emits a
 * self-contained server bundle with only the modules actually imported, so the
 * runtime image does not need node_modules. See deploy/docker/Dockerfile.dashboard.
 *
 * @type {import('next').NextConfig}
 */
const nextConfig = {
  output: 'standalone',
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
