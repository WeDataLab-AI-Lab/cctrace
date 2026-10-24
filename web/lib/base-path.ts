// The router adds the base path itself; a full page load through
// window.location does not. Empty in every build but the static demo.
const BASE_PATH = process.env.NEXT_PUBLIC_BASE_PATH ?? '';

const withBasePath = (path: string): string => `${BASE_PATH}${path}`;

export { withBasePath };
