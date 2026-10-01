'use client';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import * as React from 'react';

/**
 * Client-side providers.
 *
 * The QueryClient is created per browser session via `useState` rather than as a
 * module singleton. A module-level client is shared across requests on the
 * server, which would leak one user's cached data into another's render.
 */
export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = React.useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // Operator data goes stale quickly and a stale chart is misleading.
            // 15s matches the gateway's OTLP export interval, so a refresh
            // reflects roughly one new batch of telemetry.
            staleTime: 15_000,
            refetchOnWindowFocus: true,
            retry: (failureCount, error) => {
              // Retrying a credential or configuration failure just delays the
              // error the operator needs to see.
              const status = (error as { status?: number }).status;
              if (status && status >= 400 && status < 500) return false;
              return failureCount < 2;
            },
          },
          mutations: {
            retry: false,
          },
        },
      }),
  );

  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
