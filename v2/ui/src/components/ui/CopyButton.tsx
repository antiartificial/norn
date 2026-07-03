import { useState } from 'react'
import { Button } from './Button.tsx'

export function CopyButton({ value, label = 'Copy' }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)

  const onCopy = async () => {
    await navigator.clipboard.writeText(value)
    setCopied(true)
    window.setTimeout(() => setCopied(false), 1200)
  }

  return (
    <span className="ui-copy-wrap">
      <Button variant="ghost" size="sm" type="button" icon="fa-copy" onClick={onCopy} aria-label={`${label} to clipboard`}>
        {label}
      </Button>
      {copied && <span className="ui-copy-tip" role="status">Copied</span>}
    </span>
  )
}
