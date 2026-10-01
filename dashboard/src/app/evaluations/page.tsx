import { Suspense } from 'react';
import { EvalView } from '@/components/views/eval-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function EvaluationsPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <EvalView />
    </Suspense>
  );
}
