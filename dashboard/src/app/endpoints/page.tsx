import { Suspense } from 'react';
import { EndpointsView } from '@/components/views/endpoints-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function EndpointsPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={3} />}>
      <EndpointsView />
    </Suspense>
  );
}
