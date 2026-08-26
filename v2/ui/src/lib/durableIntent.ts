export interface DurableIntent {
  key: string
  storageKey: string
}

interface StoredIntent {
  request: string
  key: string
}

/**
 * Persists a mutation key before the request leaves the browser. Retrying after
 * a refresh therefore resolves to the original durable operation instead of
 * creating a duplicate.
 */
export function durableIntent(scope: string, request: unknown): DurableIntent {
  const storageKey = `norn:durable-intent:v1:${encodeURIComponent(scope)}`
  const serialized = JSON.stringify(request)
  try {
    const stored = JSON.parse(localStorage.getItem(storageKey) ?? 'null') as StoredIntent | null
    if (stored?.request === serialized && stored.key) return { key: stored.key, storageKey }
  } catch {
    // A corrupt or unavailable entry is safely replaced below.
  }
  const key = `norn-web-${crypto.randomUUID()}`
  try { localStorage.setItem(storageKey, JSON.stringify({ request: serialized, key })) } catch { /* best effort */ }
  return { key, storageKey }
}

export function clearDurableIntent(intent: DurableIntent): void {
  try { localStorage.removeItem(intent.storageKey) } catch { /* best effort */ }
}
