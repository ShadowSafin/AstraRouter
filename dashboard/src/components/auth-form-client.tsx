/**
 * Client wrapper for the shared authentication form.
 *
 * Lives in its own module so the page stays a server component: the page reads
 * auth state on the server, and only the interactive half needs to be client code.
 */
'use client';

import * as React from 'react';
import { useRouter } from 'next/navigation';

import { AuthForm, type AuthPolicy } from '@/components/auth-form';

interface Props {
  mode: 'setup' | 'login';
  policy: AuthPolicy;
}

interface ApiError {
  error?: { message?: string };
}

export function AuthFormClient({ mode, policy }: Props) {
  const router = useRouter();
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function onSubmit(values: { username: string; password: string; confirm: string }) {
    setBusy(true);
    setError(null);

    const endpoint = mode === 'setup' ? 'auth/setup' : 'auth/login';

    try {
      const response = await fetch(`/api/gateway/${endpoint}`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        // The session cookie is httpOnly and set by the gateway through this
        // origin, so the browser stores it without JavaScript ever seeing it.
        credentials: 'same-origin',
        body: JSON.stringify({
          username: values.username,
          password: values.password,
          confirm: values.confirm,
        }),
      });

      if (response.ok) {
        // Full navigation rather than a client-side push: the session cookie was
        // just set, and a soft navigation would render pages whose server
        // components already ran without it.
        router.replace('/');
        router.refresh();
        return;
      }

      if (response.status === 429) {
        const retry = response.headers.get('Retry-After');
        setError(
          retry
            ? `Too many attempts. Try again in about ${retry} seconds.`
            : 'Too many attempts. Try again shortly.',
        );
        setBusy(false);
        return;
      }

      const payload = (await response.json().catch(() => null)) as ApiError | null;
      // The gateway's message is already written to avoid distinguishing a wrong
      // password from an unknown user, so it is safe to show verbatim.
      setError(payload?.error?.message ?? 'Something went wrong. Try again.');
      setBusy(false);
    } catch {
      setError('The dashboard could not reach the gateway. Check that it is running.');
      setBusy(false);
    }
  }

  return <AuthForm mode={mode} policy={policy} busy={busy} error={error} onSubmit={onSubmit} />;
}
