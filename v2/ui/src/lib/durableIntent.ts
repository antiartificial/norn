export interface DurableIntent {
  key: string
  storageKey: string
  request: string
  persistence: 'local' | 'memory'
}

interface StoredIntent {
  request: string
  key: string
}

interface StoredIntentSet {
  version: 2
  intents: StoredIntent[]
}

const memoryIntents = new Map<string, Map<string, StoredIntent>>()

function parseStored(raw: string | null): StoredIntent[] {
  if (!raw) return []
  const parsed = JSON.parse(raw) as StoredIntent | StoredIntentSet | null
  if (!parsed || typeof parsed !== 'object') return []
  if ('version' in parsed && parsed.version === 2 && Array.isArray(parsed.intents)) {
    return parsed.intents.filter((intent): intent is StoredIntent => !!intent && typeof intent.request === 'string' && typeof intent.key === 'string' && intent.key.length > 0)
  }
  return 'request' in parsed && typeof parsed.request === 'string' && typeof parsed.key === 'string' && parsed.key.length > 0 ? [parsed] : []
}

function remember(storageKey: string, intents: StoredIntent[]) {
  const remembered = memoryIntents.get(storageKey) ?? new Map<string, StoredIntent>()
  for (const intent of intents) remembered.set(intent.request, intent)
  memoryIntents.set(storageKey, remembered)
}

/**
 * Persists a mutation key before the request leaves the browser. Distinct
 * payloads under one action scope retain distinct keys, so an uncertain A,
 * later B, and retry of A still converge independently.
 */
export function durableIntent(scope: string, request: unknown): DurableIntent {
  // Retain the v1 storage namespace so pending keys survive a UI upgrade. The
  // value is migrated from one StoredIntent to the v2 multi-intent envelope.
  const storageKey = `norn:durable-intent:v1:${encodeURIComponent(scope)}`
  const serialized = JSON.stringify(request)
  let stored: StoredIntent[] = []
  try {
    stored = parseStored(localStorage.getItem(storageKey))
    remember(storageKey, stored)
    const match = stored.find((intent) => intent.request === serialized && intent.key)
    if (match) return { ...match, storageKey, persistence: 'local' }
  } catch { /* fall back to the tab-local intent below */ }

  const remembered = memoryIntents.get(storageKey)?.get(serialized)
  if (remembered?.key) return { ...remembered, storageKey, persistence: 'memory' }

  const created = { request: serialized, key: `norn-web-${crypto.randomUUID()}` }
  const all = memoryIntents.get(storageKey) ?? new Map<string, StoredIntent>()
  all.set(serialized, created)
  const next = [...all.values()]
  remember(storageKey, next)
  try {
    localStorage.setItem(storageKey, JSON.stringify({ version: 2, intents: next } satisfies StoredIntentSet))
    return { ...created, storageKey, persistence: 'local' }
  } catch {
    return { ...created, storageKey, persistence: 'memory' }
  }
}

export function clearDurableIntent(intent: DurableIntent): void {
  const memory = memoryIntents.get(intent.storageKey)
  if (memory?.get(intent.request)?.key === intent.key) memory.delete(intent.request)
  if (memory?.size === 0) memoryIntents.delete(intent.storageKey)

  try {
    const stored = parseStored(localStorage.getItem(intent.storageKey))
    const next = stored.filter((candidate) => candidate.request !== intent.request || candidate.key !== intent.key)
    if (next.length === stored.length) return
    if (next.length === 0) localStorage.removeItem(intent.storageKey)
    else localStorage.setItem(intent.storageKey, JSON.stringify({ version: 2, intents: next } satisfies StoredIntentSet))
  } catch { /* best effort */ }
}
