import type { Operation } from '../types/index.ts'

const terminal = new Set(['succeeded', 'failed', 'canceled'])

export function validateIngressAcceptance(value: Operation): Operation {
  if (value.id && value.kind === 'app.cloudflared-mutate' && value.status && ['queued', 'running', ...terminal].includes(value.status)) return value
  if (!value.id && (value.status === 'unchanged' || value.status === 'skipped')) return value
  throw new Error('Server returned an invalid ingress mutation receipt')
}

export async function pollIngressOperation(
  initial: Operation,
  read: (id: string) => Promise<Operation>,
  pause: () => Promise<void> = () => new Promise((resolve) => window.setTimeout(resolve, 2_000)),
  maxPolls = 150,
): Promise<Operation> {
  const accepted = validateIngressAcceptance(initial)
  if (!accepted.id || terminal.has(accepted.status ?? '')) return accepted
  let latest = accepted
  for (let attempt = 0; attempt < maxPolls; attempt++) {
    await pause()
    try {
      const observed = await read(accepted.id)
      if (observed.id !== accepted.id || !observed.status) throw new Error('Operation identity mismatch')
      latest = observed
      if (terminal.has(observed.status)) return observed
    } catch { /* transient status read failure; retain the accepted operation */ }
  }
  return latest
}
