import { Suspense } from 'react';
import { ReplayView } from '@/components/views/eval-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function ReplayPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <ReplayView />
    </Suspense>
  );
}
