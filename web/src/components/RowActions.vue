<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import type { NodeItem } from '../api/client'
import { mediaKind } from '../lib/media'

export type RowActionKind = 'preview' | 'share' | 'rename' | 'move' | 'copy' | 'trash'

const DEFAULT_ACTIONS: RowActionKind[] = ['preview', 'share', 'rename', 'move', 'copy', 'trash']

const props = defineProps<{ node: NodeItem; actions?: RowActionKind[] }>()
const emit = defineEmits<{ action: [kind: RowActionKind, node: NodeItem] }>()

const MENU_WIDTH = 150

const isMedia = computed(() => props.node.kind === 'file' && mediaKind(props.node.name) !== null)

/** Group spaces hide actions the role/permissions do not allow (P1). */
const allowed = computed(() => new Set(props.actions ?? DEFAULT_ACTIONS))
function allows(kind: RowActionKind): boolean {
  return allowed.value.has(kind)
}

/** Enough height for the rendered items plus padding; used for flip-up. */
const MENU_HEIGHT = computed(() => 10 + allowed.value.size * 32 + (allows('trash') ? 9 : 0))

const triggerRef = ref<HTMLButtonElement | null>(null)
const menuRef = ref<HTMLElement | null>(null)
const open = ref(false)
const top = ref(0)
const left = ref(0)

const menuId = computed(() => `row-menu-${props.node.id}`)

function place(): void {
  const trigger = triggerRef.value
  if (!trigger) return
  const rect = trigger.getBoundingClientRect()
  const menu = menuRef.value
  const height = menu ? menu.offsetHeight : MENU_HEIGHT.value

  left.value = Math.max(8, Math.min(rect.right - MENU_WIDTH, window.innerWidth - MENU_WIDTH - 8))
  const below = rect.bottom + 6
  top.value = below + height > window.innerHeight - 8 ? Math.max(8, rect.top - height - 6) : below
}

async function openMenu(): Promise<void> {
  open.value = true
  place()
  await nextTick()
  place()
  document.addEventListener('pointerdown', onPointerDown, true)
  window.addEventListener('keydown', onKeydown)
  window.addEventListener('scroll', close, true)
  window.addEventListener('resize', close)
  menuRef.value?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
}

function close(): void {
  if (!open.value) return
  open.value = false
  document.removeEventListener('pointerdown', onPointerDown, true)
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('scroll', close, true)
  window.removeEventListener('resize', close)
}

function toggle(): void {
  if (open.value) close()
  else void openMenu()
}

function onPointerDown(event: PointerEvent): void {
  const target = event.target as Node | null
  if (!target) return
  if (menuRef.value?.contains(target) || triggerRef.value?.contains(target)) return
  close()
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    close()
    triggerRef.value?.focus()
  }
}

function choose(kind: RowActionKind): void {
  close()
  emit('action', kind, props.node)
}

onBeforeUnmount(close)
</script>

<template>
  <div class="row-actions">
    <button
      ref="triggerRef"
      class="row-menu-button"
      type="button"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-controls="open ? menuId : undefined"
      :aria-label="`「${node.name}」更多操作`"
      title="更多操作"
      @click.stop="toggle"
    >
      <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="3.2" cy="8" r="1.3" fill="currentColor" />
        <circle cx="8" cy="8" r="1.3" fill="currentColor" />
        <circle cx="12.8" cy="8" r="1.3" fill="currentColor" />
      </svg>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="menuId"
        ref="menuRef"
        class="row-menu"
        role="menu"
        :aria-label="`「${node.name}」操作`"
        :style="{ top: `${top}px`, left: `${left}px` }"
      >
        <button
          v-if="node.kind === 'file' && allows('preview')"
          class="row-menu-item"
          type="button"
          role="menuitem"
          @click="choose('preview')"
        >
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
            <path
              d="M1.8 8S4.1 3.8 8 3.8 14.2 8 14.2 8 11.9 12.2 8 12.2 1.8 8 1.8 8Z"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linejoin="round"
            />
            <circle cx="8" cy="8" r="1.9" fill="none" stroke="currentColor" stroke-width="1.3" />
          </svg>
          {{ isMedia ? '播放' : '预览' }}
        </button>
        <button v-if="allows('share')" class="row-menu-item" type="button" role="menuitem" @click="choose('share')">
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
            <path
              d="M6.6 9.4a2.6 2.6 0 0 0 3.7 0l2-2a2.6 2.6 0 0 0-3.7-3.7l-1 1"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
            />
            <path
              d="M9.4 6.6a2.6 2.6 0 0 0-3.7 0l-2 2a2.6 2.6 0 0 0 3.7 3.7l1-1"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
            />
          </svg>
          分享
        </button>
        <button v-if="allows('rename')" class="row-menu-item" type="button" role="menuitem" @click="choose('rename')">
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
            <path
              d="M10.9 2.9a1.4 1.4 0 0 1 2 0l.2.2a1.4 1.4 0 0 1 0 2l-7.3 7.3-2.7.7.7-2.7 7.1-7.5Z"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linejoin="round"
            />
          </svg>
          改名
        </button>
        <button v-if="allows('move')" class="row-menu-item" type="button" role="menuitem" @click="choose('move')">
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
            <path
              d="M2.2 5.6A1.4 1.4 0 0 1 3.6 4.2h2.6l1.3 1.5h5a1.4 1.4 0 0 1 1.4 1.4v4.8a1.4 1.4 0 0 1-1.4 1.4H3.6a1.4 1.4 0 0 1-1.4-1.4V5.6Z"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linejoin="round"
            />
            <path d="M9.7 9.6h3.6M11.8 7.8l1.8 1.8-1.8 1.8" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          移动
        </button>
        <button v-if="allows('copy')" class="row-menu-item" type="button" role="menuitem" @click="choose('copy')">
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
            <rect
              x="5.6"
              y="5.6"
              width="7.6"
              height="7.6"
              rx="1.3"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
            />
            <path
              d="M10.4 2.9H4.4a1.5 1.5 0 0 0-1.5 1.5v6"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
            />
          </svg>
          复制
        </button>
        <div v-if="allows('trash')" class="row-menu-sep" aria-hidden="true"></div>
        <button v-if="allows('trash')" class="row-menu-item is-danger" type="button" role="menuitem" @click="choose('trash')">
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
            <path
              d="M2.8 4.6h10.4M6.2 4.6V3.4a1 1 0 0 1 1-1h1.6a1 1 0 0 1 1 1v1.2M4.4 4.6l.6 7.6a1.2 1.2 0 0 0 1.2 1.1h3.6a1.2 1.2 0 0 0 1.2-1.1l.6-7.6"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          删除
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.row-actions {
  display: flex;
  justify-content: flex-end;
}

.row-menu-button {
  display: grid;
  width: 26px;
  height: 26px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
  opacity: 0.65;
  transition:
    background-color var(--dur) var(--ease),
    color var(--dur) var(--ease),
    opacity var(--dur) var(--ease);
}

.row-menu-button:hover,
.row-menu-button[aria-expanded='true'] {
  background: var(--color-surface);
  color: var(--color-primary-ink);
  opacity: 1;
}

.row-menu-button:focus-visible {
  opacity: 1;
}

.row-menu {
  position: fixed;
  z-index: 70;
  width: 150px;
  padding: 4px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-pop);
}

.row-menu-item {
  display: flex;
  width: 100%;
  height: 32px;
  align-items: center;
  gap: 8px;
  padding: 0 9px;
  border-radius: 5px;
  color: var(--color-text);
  font-size: var(--fs-sm);
  text-align: left;
  transition: background-color var(--dur) var(--ease);
}

.row-menu-item:hover,
.row-menu-item:focus-visible {
  background: var(--color-primary-soft);
  outline: none;
}

.row-menu-item.is-danger {
  color: var(--color-danger);
}

.row-menu-item.is-danger:hover,
.row-menu-item.is-danger:focus-visible {
  background: var(--color-danger-soft);
}

.row-menu-sep {
  height: 1px;
  margin: 4px 6px;
  background: var(--color-border);
}

@media (max-width: 700px) {
  .row-menu-button {
    width: 24px;
    height: 24px;
  }
}
</style>
