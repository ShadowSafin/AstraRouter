import type { Metadata } from 'next';
import { redirect } from 'next/navigation';

import { AuthFormClient } from '@/components/auth-form-client';
import { Login03 } from '@/components/ui/login-03';
import { authState, currentUser } from '@/lib/session';

/**
 * First-run setup: create the single console administrator.
 *
 * Reached only when the gateway reports that no operator exists and setup has
 * never been completed. Once an account exists this page redirects to login, so
 * the screen cannot be used to add a second operator or to replace the first.
 */
export const metadata: Metadata = { title: 'Initial setup · CoreRouter' };
// Auth state is per-request; a cached page would show setup to a signed-in
// operator.
export const dynamic = 'force-dynamic';

export default async function SetupPage() {
  const state = await authState();
  const user = await currentUser();

  // Already an operator: there is nothing to set up.
  if (user || (state && !state.setup_required)) {
    redirect('/login');
  }

  const policy = state?.policy ?? {
    min_password_length: 12,
    max_password_length: 256,
    max_failed_attempts: 5,
    session_minutes: 720,
  };

  return (
    <Login03
      title="Create the admin account."
      description="This console has no operator yet. The account you create here is the only one that can sign in, and it is stored as an Argon2id hash — the password itself is never written down."
      footer={
        <p className="leading-relaxed">
          This form closes once you continue. If you lose the credentials afterwards, recovering
          access needs direct database access — there is no reset link and no default account.
        </p>
      }
    >
      <AuthFormClient mode="setup" policy={policy} />
    </Login03>
  );
}
