import { Button } from './Button.tsx'
import { Modal } from './Modal.tsx'

export interface ConfirmDialogProps {
  open: boolean
  title: string
  message: string
  consequence?: string
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
  onConfirm: () => void
  onClose: () => void
}

export function ConfirmDialog({
  open,
  title,
  message,
  consequence,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  danger = false,
  onConfirm,
  onClose,
}: ConfirmDialogProps) {
  return (
    <Modal
      open={open}
      title={title}
      onClose={onClose}
      size="sm"
      footer={(
        <>
          <Button variant="ghost" onClick={onClose}>{cancelLabel}</Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm}>{confirmLabel}</Button>
        </>
      )}
    >
      <p className="ui-confirm-message">{message}</p>
      {consequence && <div className="ui-confirm-consequence">{consequence}</div>}
    </Modal>
  )
}
