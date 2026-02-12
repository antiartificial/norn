import { useState } from 'react'
import type { PodInfo } from '../types/index.ts'

function CopyBlock({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)

  const copy = () => {
    navigator.clipboard.writeText(command)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="cmd-block">
      <code>{command}</code>
      <button className="btn-icon cmd-copy" onClick={copy}>
        <i className={`fawsb ${copied ? 'fa-check' : 'fa-copy'}`} />
      </button>
    </div>
  )
}

interface Props {
  appId: string
  pods: PodInfo[]
  secrets: string[]
  onClose: () => void
}

export function CommandsModal({ appId, pods, secrets, onClose }: Props) {
  const pod = pods[0]?.name ?? `<pod>`

  return (
    <div className="welcome-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="commands-modal">
        <div className="commands-modal-header">
          <h4><i className="fawsb fa-terminal" /> Commands — {appId}</h4>
          <button className="btn-close" onClick={onClose}>&times;</button>
        </div>

        <div className="cmd-section">
          <div className="cmd-label">Shell</div>
          <CopyBlock command={`kubectl exec -it ${pod} -n default -- sh`} />
        </div>

        <div className="cmd-section">
          <div className="cmd-label">Environment</div>
          <CopyBlock command={`kubectl exec ${pod} -n default -- env | sort`} />
        </div>

        {secrets.length > 0 && (
          <div className="cmd-section">
            <div className="cmd-label">Secrets</div>
            <CopyBlock command={`norn secrets list ${appId}`} />
            {secrets.map((key) => (
              <CopyBlock key={key} command={`norn secrets get ${appId} ${key}`} />
            ))}
          </div>
        )}

        <div className="cmd-section">
          <div className="cmd-label">Logs</div>
          <CopyBlock command={`kubectl logs -f ${pod} -n default`} />
        </div>
      </div>
    </div>
  )
}
