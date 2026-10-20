const sessionAnalysisCompatibilityUrl = (source: URLSearchParams): string => {
  const params = new URLSearchParams(source);
  for (const [legacy, current] of [['from', 'trace_from'], ['to', 'trace_to']]) {
    if (!params.has(current) && params.has(legacy)) params.set(current, params.get(legacy)!);
    params.delete(legacy);
  }
  params.set('detail', 'analysis');
  params.set('expanded', '1');
  return `/sessions?${params}`;
};

export { sessionAnalysisCompatibilityUrl };
