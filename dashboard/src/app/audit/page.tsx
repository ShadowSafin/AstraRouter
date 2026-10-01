import { Suspense } from 'react';
import { AuditView } from '@/components/views/audit-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function AuditPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <AuditView />
    </Suspense>
  );
}
