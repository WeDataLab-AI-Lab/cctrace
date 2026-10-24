import { installDemoRuntime } from '@/lib/demo/runtime';

// Next.js runs this file once, before the app hydrates. Only the static demo
// build (NEXT_PUBLIC_DEMO=1) uses it: there the API answers come from recorded
// responses and the clock stands at the recording moment. In every other build
// the condition is a constant false and the demo code is not shipped.
if (process.env.NEXT_PUBLIC_DEMO === '1') {
  installDemoRuntime({
    basePath: process.env.NEXT_PUBLIC_BASE_PATH ?? '',
    recordedAt: process.env.NEXT_PUBLIC_DEMO_RECORDED_AT ?? '',
  });
}
