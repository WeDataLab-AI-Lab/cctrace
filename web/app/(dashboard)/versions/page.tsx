'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';

// Client versions moved to the Clients tab of /admin. This is a static export
// (next.config.ts: output: 'export'), so a server-side redirect() is not
// available — the effect-based replace is the documented workaround
// (FRONT-RULE: navigation side effects belong in an effect, JSDoc required).
export default function VersionsPage() {
  const router = useRouter();

  /** Bookmarked /versions links now land on the Clients tab of /admin. */
  useEffect(() => {
    router.replace('/admin');
  }, [router]);

  return null;
}
