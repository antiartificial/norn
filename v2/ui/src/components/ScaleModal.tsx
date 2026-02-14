import { useState } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface GroupInfo {
  name: string
  current: number
}

interface Props {
  appId: string
  groups: GroupInfo[]
  onClose: () => void
  onScaled: () => void
}

export function ScaleModal({ appId, groups, onClose, onScaled }: Props) {
  const [selectedGroup, setSelectedGroup] = useState(groups[0]?.name ?? '')
  const selected = groups.find(g => g.name === selectedGroup)
  const [count, setCount] = useState(selected?.current ?? 1)
  const [submitting, setSubmitting] = useState(false)

  const handleScale = async () => {
    setSubmitting(true)
    try {
      await fetch(apiUrl(`/api/apps/${appId}/scale`), {
        ...fetchOpts,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ group: selectedGroup, count }),
      })
      onScaled()
    } finally {
      setSubmitting(false)
    }
  }

  const handleGroupChange = (name: string) => {
    setSelectedGroup(name)
    const g = groups.find(g => g.name === name)
    if (g) setCount(g.current)
  }

  return (
    <div className="scale-modal-backdrop" onClick={onClose}>
      <div className="scale-modal" onClick={e => e.stopPropagation()}>
        <div className="scale-modal-header">
          <h4><i className="fawsb fa-up-right-and-down-left-from-center" /> Scale {appId}</h4>
          <button onClick={onClose} className="btn btn-close">
            <i className="fawsb fa-xmark" />
          </button>
        </div>

        {groups.length > 1 && (
          <div className="scale-modal-field">
            <label>Task group</label>
            <select
              value={selectedGroup}
              onChange={e => handleGroupChange(e.target.value)}
              className="scale-select"
            >
              {groups.map(g => (
                <option key={g.name} value={g.name}>
                  {g.name} ({g.current} running)
                </option>
              ))}
            </select>
          </div>
        )}

        {groups.length === 1 && (
          <div className="scale-modal-group-label">
            <span className="process-badge">{selectedGroup}</span>
            <span className="scale-current">{selected?.current ?? 0} running</span>
          </div>
        )}

        <div className="scale-modal-field">
          <label>New count</label>
          <input
            type="number"
            min={0}
            value={count}
            onChange={e => setCount(parseInt(e.target.value, 10) || 0)}
            className="scale-input"
            autoFocus
          />
        </div>

        <div className="scale-modal-actions">
          <button onClick={onClose} className="btn">Cancel</button>
          <button onClick={handleScale} disabled={submitting} className="btn btn-primary">
            {submitting ? <span className="btn-spinner" /> : <i className="fawsb fa-up-right-and-down-left-from-center" />}
            Scale to {count}
          </button>
        </div>
      </div>
    </div>
  )
}
