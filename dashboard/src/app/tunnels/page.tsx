import { Suspense } from 'react';
import { TunnelsView } from '@/components/views/tunnels-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function TunnelsPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <TunnelsView />
    </Suspense>
  );
}
