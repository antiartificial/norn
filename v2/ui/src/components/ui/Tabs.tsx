import { Children, createContext, isValidElement, useCallback, useContext, useId, useMemo, useState, type KeyboardEvent, type ReactElement, type ReactNode } from 'react'

interface TabsContextValue {
  value: string
  setValue: (value: string) => void
  baseId: string
}

const TabsContext = createContext<TabsContextValue | null>(null)

export function Tabs({ value, defaultValue, onValueChange, children }: { value?: string; defaultValue?: string; onValueChange?: (value: string) => void; children: ReactNode }) {
  const baseId = useId()
  const [internalValue, setInternalValue] = useState(defaultValue ?? '')
  const current = value ?? internalValue
  const setValue = useCallback((next: string) => {
    setInternalValue(next)
    onValueChange?.(next)
  }, [onValueChange])
  const context = useMemo(() => ({ value: current, setValue, baseId }), [baseId, current, setValue])
  return <TabsContext.Provider value={context}>{children}</TabsContext.Provider>
}

function useTabs() {
  const value = useContext(TabsContext)
  if (!value) throw new Error('Tabs components must be used inside Tabs')
  return value
}

export function TabsList({ children, 'aria-label': ariaLabel }: { children: ReactNode; 'aria-label'?: string }) {
  const { setValue } = useTabs()
  const items = Children.toArray(children).filter(isValidElement) as ReactElement<TabProps>[]

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowRight', 'ArrowLeft', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const current = document.activeElement?.getAttribute('data-tab-value')
    const values = items.map((item) => item.props.value)
    const index = Math.max(0, values.indexOf(current ?? values[0]))
    const nextIndex = event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? values.length - 1
        : event.key === 'ArrowRight'
          ? (index + 1) % values.length
          : (index - 1 + values.length) % values.length
    const next = values[nextIndex]
    setValue(next)
    const tabs = Array.from(document.querySelectorAll<HTMLElement>('[data-tab-value]'))
    tabs.find((tab) => tab.dataset.tabValue === next)?.focus()
  }

  return <div className="ui-tabs-list" role="tablist" aria-label={ariaLabel} onKeyDown={onKeyDown}>{children}</div>
}

export interface TabProps {
  value: string
  children: ReactNode
}

export function Tab({ value, children }: TabProps) {
  const { value: current, setValue, baseId } = useTabs()
  const selected = current === value
  return (
    <button
      id={`${baseId}-tab-${value}`}
      className="ui-tab"
      role="tab"
      type="button"
      aria-selected={selected}
      aria-controls={`${baseId}-panel-${value}`}
      tabIndex={selected ? 0 : -1}
      data-tab-value={value}
      onClick={() => setValue(value)}
    >
      {children}
    </button>
  )
}

export function TabPanel({ value, children }: { value: string; children: ReactNode }) {
  const { value: current, baseId } = useTabs()
  if (current !== value) return null
  return (
    <div id={`${baseId}-panel-${value}`} className="ui-tab-panel" role="tabpanel" aria-labelledby={`${baseId}-tab-${value}`}>
      {children}
    </div>
  )
}
