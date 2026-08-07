import { useEffect, useRef } from 'react'

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'textarea:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

interface FocusLayer {
  id: symbol
  node: HTMLElement
  onClose: () => void
}

const layerStack: FocusLayer[] = []
let listening = false

function onDocumentKeyDown(event: KeyboardEvent) {
  const layer = layerStack[layerStack.length - 1]
  if (!layer) return
  const node = layer.node
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopImmediatePropagation()
    layer.onClose()
    return
  }
  if (event.key !== 'Tab') return
  event.stopImmediatePropagation()
  const items = getFocusable(node)
  if (items.length === 0) {
    event.preventDefault()
    node.focus()
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function ensureKeyListener() {
  if (listening) return
  document.addEventListener('keydown', onDocumentKeyDown)
  listening = true
}

function releaseKeyListener() {
  if (layerStack.length > 0 || !listening) return
  document.removeEventListener('keydown', onDocumentKeyDown)
  listening = false
}

export function getFocusable(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(focusableSelector))
    .filter((element) => !element.hasAttribute('disabled') && element.getAttribute('aria-hidden') !== 'true' && !element.hidden)
}

export function useFocusTrap(open: boolean, onClose: () => void) {
  const ref = useRef<HTMLDivElement>(null)
  const returnFocusRef = useRef<HTMLElement | null>(null)
  const onCloseRef = useRef(onClose)

  useEffect(() => {
    onCloseRef.current = onClose
  }, [onClose])

  useEffect(() => {
    if (!open) return
    const layerId = Symbol('focus-layer')
    returnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const node = ref.current
    if (!node) return
    layerStack.push({ id: layerId, node, onClose: () => onCloseRef.current() })
    ensureKeyListener()
    const focusables = getFocusable(node)
    ;(focusables[0] ?? node).focus()
    return () => {
      const index = layerStack.findIndex((layer) => layer.id === layerId)
      if (index >= 0) layerStack.splice(index, 1)
      releaseKeyListener()
      returnFocusRef.current?.focus()
    }
  }, [open])

  return ref
}
