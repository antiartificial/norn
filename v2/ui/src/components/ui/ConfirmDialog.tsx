import { Button } from './Button.tsx'
import { Modal } from './Modal.tsx'

export interface ConfirmDialogProps {
  open: boolean
  title: string
  message: string
  consequence?: string
  confirmLabel?: string
  confirmIcon?: string
  cancelLabel?: string
  danger?: boolean
  onConfirm: () => void
  onClose: () => void
}

function destructiveIcon(label: string): string {
  return /(rollback|restore)/i.test(label) ? 'fa-arrow-rotate-left' : 'fa-trash'
}

export function ConfirmDialog({
  open,
  title,
  message,
  consequence,
  confirmLabel = 'Confirm',
  confirmIcon,
  cancelLabel = 'Cancel',
  danger = false,
  onConfirm,
  onClose,
}: ConfirmDialogProps) {
  const icon = confirmIcon ?? (danger ? destructiveIcon(confirmLabel) : 'fa-check')

  return (
    <Modal
      open={open}
      title={title}
      onClose={onClose}
      size="sm"
      footer={(
        <>
          <Button variant="ghost" icon="fa-xmark" onClick={onClose}>{cancelLabel}</Button>
          <Button variant={danger ? 'danger' : 'primary'} icon={icon} onClick={onConfirm}>{confirmLabel}</Button>
        </>
      )}
    >
      <p className="ui-confirm-message">{message}</p>
      {consequence && <div className="ui-confirm-consequence">{consequence}</div>}
    </Modal>
  )
}
