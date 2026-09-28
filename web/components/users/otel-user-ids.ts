const fetchOtelUserIDs = async (): Promise<string[]> => {
  const res = await fetch('/api/admin/otel-user-ids', { credentials: 'include' });
  if (!res.ok) return [];
  return res.json();
};

export { fetchOtelUserIDs };
