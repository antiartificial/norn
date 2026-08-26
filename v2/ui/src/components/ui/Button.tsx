import type { ButtonHTMLAttributes, ReactNode } from 'react'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'sm' | 'md'
  loading?: boolean
  icon?: string
  children?: ReactNode
}

export function Button({ variant = 'secondary', size = 'md', loading = false, icon, className = '', children, disabled, ...props }: ButtonProps) {
  return (
    <button
      className={`ui-button ui-button-${variant} ui-button-${size} ${className}`.trim()}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {loading && <span className="ui-button-spinner" aria-hidden="true" />}
      {!loading && icon && <i className={`fawsb ${icon}`} aria-hidden="true" />}
      {children}
    </button>
  )
}
