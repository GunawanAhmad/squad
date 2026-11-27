// Shared fetch helper: JSON in, JSON out, the server's text as the error.
export async function api<T>(path: string, init?: Parameters<typeof fetch>[1]): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}
