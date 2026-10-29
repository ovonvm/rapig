import { reactive } from 'vue'

export type ToastTone = 'success' | 'error' | 'undo'

export interface ToastAction {
  label: string
  run: () => void
}

export interface Toast {
  id: number
  message: string
  tone: ToastTone
  action: ToastAction | null
}

const toasts = reactive<Toast[]>([])
let nextId = 1

function dismiss(id: number): void {
  const index = toasts.findIndex((toast) => toast.id === id)
  if (index !== -1) toasts.splice(index, 1)
}

/**
 * Show a short-lived message. Pass an action for undo/“view tasks” style toasts;
 * action toasts live a little longer so the button can actually be clicked.
 */
function push(
  message: string,
  tone: ToastTone = 'success',
  duration = 2600,
  action: ToastAction | null = null,
): number {
  const id = nextId++
  toasts.push({ id, message, tone, action })

  if (toasts.length > 3) toasts.shift()
  window.setTimeout(() => dismiss(id), duration)

  return id
}

function runAction(toast: Toast): void {
  const action = toast.action
  dismiss(toast.id)
  action?.run()
}

/** Module-level toast queue shared by every caller of useToast(). */
export function useToast() {
  return {
    toasts,
    push,
    dismiss,
    runAction,
  }
}
