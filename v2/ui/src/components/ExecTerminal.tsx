import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { wsUrl } from '../lib/api.ts'

interface Props {
  appId: string
  onClose: () => void
}

export function ExecTerminal({ appId, onClose }: Props) {
  const termRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (!termRef.current) return

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'JetBrains Mono', 'Fira Code', monospace",
      theme: {
        background: '#0f1117',
        foreground: '#e4e6ed',
        cursor: '#e4e6ed',
        selectionBackground: 'rgba(167, 139, 250, 0.3)',
      },
    })

    const fitAddon = new FitAddon()
    terminal.loadAddon(fitAddon)
    terminal.open(termRef.current)
    fitAddon.fit()
    terminalRef.current = terminal

    // Build WebSocket URL — exec endpoint is under /api, not /ws
    const base = wsUrl() // gives ws://host/ws
    const wsBase = base.replace(/\/ws$/, '')
    const execUrl = `${wsBase}/api/apps/${appId}/exec?command=/bin/sh`

    const ws = new WebSocket(execUrl)
    wsRef.current = ws

    ws.onopen = () => {
      // Send initial terminal size
      const msg = JSON.stringify({
        resize: { width: terminal.cols, height: terminal.rows },
      })
      ws.send(msg)
    }

    ws.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data)
        if (data.stdout) terminal.write(data.stdout)
        if (data.stderr) terminal.write(data.stderr)
        if (data.exit !== undefined) {
          terminal.write(`\r\n[Process exited with code ${data.exit}]\r\n`)
        }
      } catch {
        // ignore
      }
    }

    ws.onclose = () => {
      terminal.write('\r\n[Connection closed]\r\n')
    }

    ws.onerror = () => {
      terminal.write('\r\n[Connection error]\r\n')
    }

    // Send keystrokes to backend
    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ stdin: data }))
      }
    })

    // Send resize events
    terminal.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ resize: { width: cols, height: rows } }))
      }
    })

    // Handle window resize
    const handleResize = () => fitAddon.fit()
    window.addEventListener('resize', handleResize)

    terminal.focus()

    return () => {
      window.removeEventListener('resize', handleResize)
      ws.close()
      terminal.dispose()
    }
  }, [appId])

  return (
    <div className="exec-terminal">
      <div className="exec-terminal-header">
        <h3>
          <i className="fawsb fa-terminal" /> {appId}
        </h3>
        <button onClick={onClose} className="btn btn-close">
          <i className="fawsb fa-xmark" />
        </button>
      </div>
      <div ref={termRef} className="exec-terminal-body" />
    </div>
  )
}
