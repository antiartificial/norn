import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { useFocusTrap } from '../components/ui/focus.ts'
import { useToast } from '../components/ui/index.ts'
import type { AppAction, ActivityEntry } from '../runtime/AppRuntime.tsx'
import type { ActiveIncidentsResponse, AppStatus } from '../types/index.ts'

export function CommandPalette({ open, onClose, apps, activity, runAction }: { open: boolean; onClose: () => void; apps: AppStatus[]; activity: ActivityEntry[]; runAction: (appId: string, action: AppAction) => void }) {
  const [query, setQuery] = useState('')
  const [index, setIndex] = useState(0)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const ref = useFocusTrap(open, onClose)
  const inputRef = useRef<HTMLInputElement>(null)

  const ackAllIncidents = async () => {
    const active = await apiFetch<ActiveIncidentsResponse>('/api/events/active')
    const results = await Promise.allSettled(active.incidents.map((incident) => apiFetch(`/api/events/${incident.latestEventId}/ack`, { method: 'POST' })))
    const acked = results.filter((result) => result.status === 'fulfilled').length
    const failed = results.length - acked
    toast({
      kind: failed ? 'info' : 'success',
      title: `Acked ${acked} incident${acked === 1 ? '' : 's'}`,
      description: failed ? `${failed} failed` : undefined,
    })
    queryClient.invalidateQueries({ queryKey: ['events'] })
  }

  const commands = useMemo(() => {
    const viewItems = [
      { id: 'view-overview', label: 'Overview', detail: 'Jump to /overview', run: () => navigate('/overview') },
      { id: 'view-apps', label: 'Apps', detail: 'Jump to /apps', run: () => navigate('/apps') },
      { id: 'view-deploys', label: 'Deploys', detail: 'Jump to /deploys', run: () => navigate('/deploys') },
      { id: 'view-incidents', label: 'Incidents', detail: 'Jump to /incidents', run: () => navigate('/incidents') },
      { id: 'view-operations', label: 'Operations', detail: 'Jump to /operations', run: () => navigate('/operations') },
      { id: 'view-topology', label: 'Topology', detail: 'Jump to /topology', run: () => navigate('/topology') },
      { id: 'ack-incidents', label: 'Ack all incidents', detail: 'Acknowledge currently active incidents', run: ackAllIncidents },
    ]
    const appItems = apps.flatMap((app) => {
      const id = app.spec.name
      return [
        { id: `app-${id}`, label: id, detail: 'Open app detail', run: () => navigate(`/apps/${id}/overview`) },
        { id: `deploy-${id}`, label: `Deploy ${id}`, detail: 'Deploy latest HEAD', run: () => runAction(id, 'deploy') },
        { id: `restart-${id}`, label: `Restart ${id}`, detail: 'Rolling restart', run: () => runAction(id, 'restart') },
      ]
    })
    return [...viewItems, ...appItems]
  }, [apps, navigate, runAction])
  const filtered = useMemo(() => {
    const q = query.toLowerCase().replace(/\s+/g, '')
    if (!q) return commands
    return commands.filter((command) => command.label.toLowerCase().replace(/\s+/g, '').includes(q) || command.detail.toLowerCase().includes(query.toLowerCase()))
  }, [commands, query])

  useEffect(() => {
    if (!open) return
    setQuery('')
    setIndex(0)
    window.setTimeout(() => inputRef.current?.focus(), 0)
  }, [open])

  useEffect(() => setIndex(0), [query])
  if (!open) return null

  const runSelected = () => {
    const command = filtered[index]
    if (!command) return
    void command.run()
    onClose()
  }
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setIndex((cur) => Math.min(filtered.length - 1, cur + 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setIndex((cur) => Math.max(0, cur - 1))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      runSelected()
    }
  }

  return (
    <div className="palette-backdrop">
      <div className="command-palette" role="dialog" aria-modal="true" aria-label="Command palette" ref={ref} tabIndex={-1} onKeyDown={onKeyDown}>
        <div className="palette-input-wrap">
          <i className="fawsb fa-magnifying-glass" aria-hidden />
          <input ref={inputRef} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search apps, views, actions" aria-controls="command-results" />
        </div>
        <div id="command-results" className="palette-results" role="listbox" aria-label="Commands">
          {filtered.length === 0 ? <div className="palette-empty">No matches</div> : filtered.map((command, i) => (
            <button
              key={command.id}
              type="button"
              role="option"
              aria-selected={i === index}
              className={`palette-option ${i === index ? 'active' : ''}`}
              onMouseEnter={() => setIndex(i)}
              onClick={runSelected}
            >
              <span>{command.label}</span>
              <small>{command.detail}</small>
            </button>
          ))}
        </div>
        <div className="palette-footer">{activity.length} recent hub events</div>
      </div>
    </div>
  )
}
