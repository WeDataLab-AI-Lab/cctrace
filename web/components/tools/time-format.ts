const truncate = (s: string, max: number): string =>
  s.length > max ? s.slice(0, max) + '...' : s;

export { truncate };
