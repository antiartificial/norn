import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

export type ToastKind = 'success' | 'error' | 'info'

export interface ToastInput {
  kind?: ToastKind
  title: string
  description?: string
  durationMs?: number
}

interface ToastRecord extends Required<Pick<ToastInput, 'kind' | 'durationMs'>> {
  id: string
  title: string
  description?: string
}

interface ToastContextValue {
  toast: (input: ToastInput) => string
  dismiss: (id: string) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastRecord[]>([])
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>())
  const paused = useRef(new Set<string>())

  const dismiss = useCallback((id: string) => {
    const timer = timers.current.get(id)
    if (timer) clearTimeout(timer)
    timers.current.delete(id)
    paused.current.delete(id)
    setToasts((items) => items.filter((item) => item.id !== id))
  }, [])

  const schedule = useCallback((id: string, durationMs: number) => {
    const timer = setTimeout(() => dismiss(id), durationMs)
    timers.current.set(id, timer)
  }, [dismiss])

  const toast = useCallback((input: ToastInput) => {
    const id = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
    const record: ToastRecord = {
      id,
      kind: input.kind ?? 'info',
      title: input.title,
      description: input.description,
      durationMs: input.durationMs ?? 4000,
    }
    setToasts((items) => [...items, record])
    schedule(id, record.durationMs)
    return id
  }, [schedule])

  useEffect(() => {
    const currentTimers = timers.current
    return () => {
      for (const timer of currentTimers.values()) clearTimeout(timer)
      currentTimers.clear()
      paused.current.clear()
    }
  }, [])

  const value = useMemo(() => ({ toast, dismiss }), [dismiss, toast])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="ui-toast-viewport" aria-live="polite" aria-atomic="false">
        {toasts.map((item) => (
          <div
            key={item.id}
            className={`ui-toast ui-toast-${item.kind}`}
            role="status"
            onMouseEnter={() => {
              paused.current.add(item.id)
              const timer = timers.current.get(item.id)
              if (timer) clearTimeout(timer)
            }}
            onMouseLeave={() => {
              if (!paused.current.has(item.id)) return
              paused.current.delete(item.id)
              schedule(item.id, item.durationMs)
            }}
            onFocus={() => {
              paused.current.add(item.id)
              const timer = timers.current.get(item.id)
              if (timer) clearTimeout(timer)
            }}
            onBlur={(event) => {
              if (event.currentTarget.contains(event.relatedTarget)) return
              if (!paused.current.has(item.id)) return
              paused.current.delete(item.id)
              schedule(item.id, item.durationMs)
            }}
          >
            <p className="ui-toast-title">{item.title}</p>
            {item.description && <p className="ui-toast-description">{item.description}</p>}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const value = useContext(ToastContext)
  if (!value) throw new Error('useToast must be used within ToastProvider')
  return value
}
