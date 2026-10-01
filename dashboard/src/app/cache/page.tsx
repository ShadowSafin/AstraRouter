import { Suspense } from 'react';
import { CacheView } from '@/components/views/cache-view';
import { CardsSkeleton } from '@/components/ui/state';

export default function CachePage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <CacheView />
    </Suspense>
  );
}
