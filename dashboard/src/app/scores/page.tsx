import { Suspense } from 'react';
import { ScoresView } from '@/components/views/scores-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function ScoresPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <ScoresView />
    </Suspense>
  );
}
