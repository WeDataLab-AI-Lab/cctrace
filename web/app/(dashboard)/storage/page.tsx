'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';

// Storage moved to the Storage tab of /admin. This is a static export
// (next.config.ts: output: 'export'), so a server-side redirect() is not
// available — the effect-based replace is the documented workaround
// (FRONT-RULE: navigation side effects belong in an effect, JSDoc required).
export default function StoragePage() {
  const router = useRouter();

  /** Bookmarked /storage links now land on the Storage tab of /admin. */
  useEffect(() => {
    router.replace('/admin');
  }, [router]);

  return null;
}
