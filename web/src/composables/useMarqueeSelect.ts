import { onBeforeUnmount, ref, type Ref } from 'vue'

/** Selection rectangle in container-relative CSS pixels. */
export interface MarqueeRect {
  left: number
  top: number
  width: number
  height: number
}

export interface MarqueeOptions {
  /** Element the marquee is drawn inside; it must be positioned. */
  container: Ref<HTMLElement | null>
  /** Selection before the drag, used as the base for Ctrl/Cmd additive drags. */
  selectedIds: Ref<number[]>
  /** Receives the live selection while dragging and the final selection on release. */
  apply: (ids: number[]) => void
}

/** Pointer travel below this is treated as a click, not a drag. */
const DRAG_THRESHOLD = 4
/** Pressing any of these starts that element's own interaction instead of a marquee. */
const INTERACTIVE =
  'button, a, input, textarea, select, [role="menu"], [role="menuitem"], .row, .card, .list-head, .skeleton, .state, .empty, .page-head, .queue, .tip'

/**
 * Desktop marquee ("rubber band") selection over `[data-node-id]` elements.
 * Touch pointers are ignored: mobile relies on tap-to-toggle instead of dragging.
 */
export function useMarqueeSelect(options: MarqueeOptions) {
  const rect = ref<MarqueeRect | null>(null)
  const dragging = ref(false)

  let startX = 0
  let startY = 0
  let pointerX = 0
  let pointerY = 0
  let base: number[] = []
  let additive = false
  let pressed = false
  let previousUserSelect = ''

  function bounds(): DOMRect | null {
    return options.container.value?.getBoundingClientRect() ?? null
  }

  function hitIds(box: MarqueeRect): number[] {
    const container = options.container.value
    if (!container) return []

    const containerRect = container.getBoundingClientRect()
    const ids: number[] = []

    for (const element of container.querySelectorAll<HTMLElement>('[data-node-id]')) {
      const nodeRect = element.getBoundingClientRect()
      const left = nodeRect.left - containerRect.left
      const top = nodeRect.top - containerRect.top
      const right = left + nodeRect.width
      const bottom = top + nodeRect.height

      const intersects =
        left < box.left + box.width &&
        right > box.left &&
        top < box.top + box.height &&
        bottom > box.top
      if (!intersects) continue

      const id = Number(element.dataset.nodeId)
      if (Number.isInteger(id)) ids.push(id)
    }

    return ids
  }

  function paint(): void {
    const container = options.container.value
    const containerRect = bounds()
    if (!container || !containerRect) return

    const left = Math.min(startX, pointerX)
    const top = Math.min(startY, pointerY)
    const box: MarqueeRect = {
      left,
      top,
      width: Math.abs(pointerX - startX),
      height: Math.abs(pointerY - startY),
    }
    rect.value = box

    const hits = hitIds(box)
    const merged = additive ? Array.from(new Set([...base, ...hits])) : hits
    options.apply(merged)
  }

  function setUserSelect(disabled: boolean): void {
    if (disabled) {
      previousUserSelect = document.body.style.userSelect
      document.body.style.userSelect = 'none'
      return
    }
    document.body.style.userSelect = previousUserSelect
  }

  function stop(): void {
    pressed = false
    dragging.value = false
    rect.value = null
    setUserSelect(false)
    window.removeEventListener('pointermove', onPointerMove)
    window.removeEventListener('pointerup', onPointerUp)
    window.removeEventListener('pointercancel', onPointerUp)
    window.removeEventListener('keydown', onKeyDown, true)
  }

  function onPointerMove(event: PointerEvent): void {
    if (!pressed) return
    const containerRect = bounds()
    if (!containerRect) return

    pointerX = event.clientX - containerRect.left
    pointerY = event.clientY - containerRect.top

    if (!dragging.value) {
      if (Math.hypot(pointerX - startX, pointerY - startY) < DRAG_THRESHOLD) return
      dragging.value = true
      setUserSelect(true)
    }

    event.preventDefault()
    paint()
  }

  function onPointerUp(): void {
    if (!pressed) return
    const wasDragging = dragging.value
    stop()
    if (wasDragging) options.apply(options.selectedIds.value.slice())
  }

  function onKeyDown(event: KeyboardEvent): void {
    if (event.key !== 'Escape' || !dragging.value) return
    event.preventDefault()
    event.stopPropagation()
    // Esc restores the selection captured when the drag started.
    options.apply(base.slice())
    stop()
  }

  function onPointerDown(event: PointerEvent): void {
    if (event.button !== 0 || event.pointerType === 'touch') return
    const target = event.target instanceof Element ? event.target : null
    if (!target || target.closest(INTERACTIVE)) return

    const containerRect = bounds()
    if (!containerRect) return

    startX = event.clientX - containerRect.left
    startY = event.clientY - containerRect.top
    pointerX = startX
    pointerY = startY
    base = options.selectedIds.value.slice()
    additive = event.ctrlKey || event.metaKey
    pressed = true

    window.addEventListener('pointermove', onPointerMove)
    window.addEventListener('pointerup', onPointerUp)
    window.addEventListener('pointercancel', onPointerUp)
    window.addEventListener('keydown', onKeyDown, true)
  }

  function cancel(): void {
    if (!pressed) return
    options.apply(base.slice())
    stop()
  }

  onBeforeUnmount(stop)

  return { rect, dragging, onPointerDown, cancel }
}
