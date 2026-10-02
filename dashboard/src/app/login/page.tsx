import type { Metadata } from 'next';
import { redirect } from 'next/navigation';

import { AuthFormClient } from '@/components/auth-form-client';
import { Login03 } from '@/components/ui/login-03';
import { authState, currentUser } from '@/lib/session';

/**
 * Operator sign-in.
 *
 * Sends a fresh installation to setup instead, so a new deployment lands on the
 * one screen that can do something useful rather than on a login form that cannot
 * succeed yet.
 */
export const metadata: Metadata = { title: 'Sign in · CoreRouter' };
export const dynamic = 'force-dynamic';

export default async function LoginPage() {
  const [state, user] = await Promise.all([authState(), currentUser()]);

  if (user) {
    redirect('/');
  }
  if (state?.setup_required) {
    redirect('/setup');
  }

  const policy = state?.policy ?? {
    min_password_length: 12,
    max_password_length: 256,
    max_failed_attempts: 5,
    session_minutes: 720,
  };

  return (
    <Login03
      title="Sign in."
      description="The console is the control plane for routing, spend and providers."
      footer={
        <p className="leading-relaxed">
          After {policy.max_failed_attempts} failed attempts the account locks temporarily, with the
          delay doubling on each further round.
        </p>
      }
    >
      <AuthFormClient mode="login" policy={policy} />
    </Login03>
  );
}
