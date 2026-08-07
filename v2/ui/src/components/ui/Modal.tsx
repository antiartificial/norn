import type { ReactNode } from 'react'
import { useId } from 'react'
import { createPortal } from 'react-dom'
import { useFocusTrap } from './focus.ts'

export interface ModalProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  size?: 'sm' | 'md' | 'lg'
}

export function Modal({ open, title, onClose, children, footer, size = 'md' }: ModalProps) {
  const titleId = useId()
  const ref = useFocusTrap(open, onClose)
  if (!open) return null

  return createPortal(
    <div className="ui-layer-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <div ref={ref} className={`ui-modal ui-modal-${size}`} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1}>
        <div className="ui-layer-header">
          <h2 className="ui-layer-title" id={titleId}>{title}</h2>
          <button className="ui-layer-close" type="button" aria-label="Close modal" onClick={onClose}>×</button>
        </div>
        <div className="ui-layer-body">{children}</div>
        {footer && <div className="ui-layer-footer">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}
