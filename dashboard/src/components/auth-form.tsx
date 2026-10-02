'use client';

/**
 * The console's authentication form, shared by first-run setup and login.
 *
 * One component rather than two screens' worth of duplication: the fields are the
 * same, the only difference is whether a confirmation is shown and which endpoint
 * is called. Splitting them would mean two places to fix the strength meter.
 *
 * The component never sees a password after submit. It hands the values to the
 * caller and clears them.
 */
import * as React from 'react';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

export interface AuthPolicy {
  min_password_length: number;
  max_password_length: number;
  max_failed_attempts: number;
}

interface Props {
  mode: 'setup' | 'login';
  policy: AuthPolicy;
  busy?: boolean;
  /** Server-rendered message, e.g. "the username or password is incorrect". */
  error?: string | null;
  onSubmit: (values: { username: string; password: string; confirm: string }) => void;
}

/** Score a password against the policy, returning 0..4 plus a label. */
function scorePassword(password: string, policy: AuthPolicy): { score: number; label: string } {
  if (!password) return { score: 0, label: '' };

  let score = 0;
  if (password.length >= policy.min_password_length) score += 1;
  if (password.length >= policy.min_password_length + 8) score += 1;
  if (/[a-z]/.test(password) && /[A-Z0-9]/.test(password)) score += 1;
  if (/[^A-Za-z0-9]/.test(password)) score += 1;

const labels: Record<number, string> = {
    0: 'Too short',
    1: 'Weak',
    2: 'Fair',
    3: 'Good',
    4: 'Strong',
  };
  // `noUncheckedIndexedAccess` makes an array lookup `string | undefined` even for
  // in-range keys, so the score is mapped through a record instead. The fallback
  // is unreachable: score is clamped to the meter width above.
  return { score, label: labels[score] ?? labels[0]! };
}

const BAR_COLORS = [
  'bg-danger/70',
  'bg-danger/70',
  'bg-warn/70',
  'bg-primary/70',
  'bg-success/70',
];

export function AuthForm({ mode, policy, busy = false, error, onSubmit }: Props) {
  const isSetup = mode === 'setup';
  const [username, setUsername] = React.useState('');
  const [password, setPassword] = React.useState('');
  const [confirm, setConfirm] = React.useState('');
  const [localError, setLocalError] = React.useState<string | null>(null);

  const strength = React.useMemo(() => scorePassword(password, policy), [password, policy]);
  const mismatch = isSetup && confirm.length > 0 && confirm !== password;

  function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    setLocalError(null);

    if (isSetup) {
      // Checked here for a fast, friendly message. The gateway revalidates every
      // rule, because a client-side check is a convenience and not a control.
      if (password.length < policy.min_password_length) {
        setLocalError(`Use at least ${policy.min_password_length} characters.`);
        return;
      }
      if (password !== confirm) {
        setLocalError('The passwords do not match.');
        return;
      }
    }

    onSubmit({ username, password, confirm });
    setPassword('');
    setConfirm('');
  }

  const message = localError ?? error ?? null;

  return (
    <form onSubmit={handleSubmit} className="space-y-4" noValidate>
      <div className="space-y-1.5">
        <label htmlFor="username" className="text-sm font-medium text-foreground">
          Username
        </label>
        <Input
          id="username"
          name="username"
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          required
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          placeholder="admin"
          className="h-10"
        />
      </div>

      <div className="space-y-1.5">
        <label htmlFor="password" className="text-sm font-medium text-foreground">
          Password
        </label>
        <Input
          id="password"
          name="password"
          type="password"
          // Not "current-password" on the setup screen: there is no current
          // password yet, and telling the password manager otherwise makes it
          // offer a saved credential that does not exist.
          autoComplete={isSetup ? 'new-password' : 'current-password'}
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          className="h-10"
        />

        {isSetup && password.length > 0 ? (
          <div className="space-y-1 pt-1">
            <div className="flex gap-1" aria-hidden="true">
              {[0, 1, 2, 3].map((index) => (
                <span
                  key={index}
                  className={`h-1 flex-1 rounded-full transition-colors ${
                    index < strength.score ? BAR_COLORS[strength.score] : 'bg-muted'
                  }`}
                />
              ))}
            </div>
            <p className="text-[11px] text-muted-foreground">
              {strength.label} · at least {policy.min_password_length} characters
            </p>
          </div>
        ) : null}
      </div>

      {isSetup ? (
        <div className="space-y-1.5">
          <label htmlFor="confirm" className="text-sm font-medium text-foreground">
            Confirm password
          </label>
          <Input
            id="confirm"
            name="confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
            aria-invalid={mismatch}
            className="h-10"
          />
          {mismatch ? (
            <p className="text-[11px] text-danger">The passwords do not match.</p>
          ) : null}
        </div>
      ) : null}

      {message ? (
        <p
          role="alert"
          className="rounded-lg border border-danger/40 bg-danger/10 px-3 py-2 text-[13px] text-danger"
        >
          {message}
        </p>
      ) : null}

      <Button type="submit" disabled={busy} className="h-10 w-full justify-center">
        {busy ? 'Working…' : isSetup ? 'Create admin account' : 'Sign in'}
      </Button>
    </form>
  );
}
