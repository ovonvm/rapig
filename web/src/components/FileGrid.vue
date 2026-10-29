<script setup lang="ts">
import RowActions from './RowActions.vue'
import type { RowActionKind } from './RowActions.vue'
import { formatBytes } from '../lib/format'
import type { NodeItem } from '../api/client'

defineProps<{
  items: NodeItem[]
  selectedIds: ReadonlySet<number>
  /** Restrict the card action menu; omitted means every default action. */
  actions?: RowActionKind[]
}>()

const emit = defineEmits<{
  /** Corner checkbox: accumulates/toggles the selection. */
  toggle: [id: number, event: MouseEvent]
  /** Blank card click: plain single selection (folders open instead). */
  select: [node: NodeItem, event: MouseEvent]
  action: [kind: RowActionKind, node: NodeItem]
  open: [node: NodeItem]
  /** Double click on a file card opens the preview overlay. */
  preview: [node: NodeItem]
}>()

function sizeLabel(node: NodeItem): string {
  return node.kind === 'dir' ? '—' : formatBytes(node.size)
}

/** A card click opens folders; files fall back to plain single selection. */
function onCardClick(node: NodeItem, event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('button, a, input, [role="menu"], [role="menuitem"]')) return
  if (node.kind === 'dir') {
    emit('open', node)
    return
  }
  emit('select', node, event)
}

/** Double click previews files; folders keep their single-click navigation. */
function onCardDblClick(node: NodeItem, event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('button, a, input, [role="menu"], [role="menuitem"]')) return
  if (node.kind !== 'file') return
  event.preventDefault()
  emit('preview', node)
}
</script>

<template>
  <ul class="file-grid" aria-label="文件网格">
    <li
      v-for="node in items"
      :key="node.id"
      class="card"
      :class="{ 'is-selected': selectedIds.has(node.id), 'is-dir': node.kind === 'dir' }"
      :data-node-id="node.id"
      @click="onCardClick(node, $event)"
      @dblclick="onCardDblClick(node, $event)"
    >
      <!-- The hit target is 44x44 for touch, while the box stays 18x18. -->
      <button
        type="button"
        class="card-check-hit"
        role="checkbox"
        :aria-checked="selectedIds.has(node.id) ? 'true' : 'false'"
        :aria-label="`选择 ${node.name}`"
        @click.prevent.stop="emit('toggle', node.id, $event)"
      >
        <span class="card-check-box">
          <svg viewBox="0 0 12 12" aria-hidden="true">
            <path
              d="M2.2 6.3 4.8 8.8 9.8 3.4"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </span>
      </button>

      <span class="card-menu">
        <RowActions :node="node" :actions="actions" @action="(kind, target) => emit('action', kind, target)" />
      </span>

      <span class="card-icon" :class="node.kind === 'dir' ? 'is-dir' : 'is-file'">
        <svg v-if="node.kind === 'dir'" width="30" height="30" viewBox="0 0 20 20" aria-hidden="true">
          <path
            d="M2.75 6.25A1.75 1.75 0 0 1 4.5 4.5h3.2l1.55 1.75h6.25a1.75 1.75 0 0 1 1.75 1.75v6.25a1.75 1.75 0 0 1-1.75 1.75H4.5a1.75 1.75 0 0 1-1.75-1.75V6.25Z"
            fill="currentColor"
            opacity="0.16"
          />
          <path
            d="M2.75 6.25A1.75 1.75 0 0 1 4.5 4.5h3.2l1.55 1.75h6.25a1.75 1.75 0 0 1 1.75 1.75v6.25a1.75 1.75 0 0 1-1.75 1.75H4.5a1.75 1.75 0 0 1-1.75-1.75V6.25Z"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linejoin="round"
          />
        </svg>
        <svg v-else width="30" height="30" viewBox="0 0 20 20" aria-hidden="true">
          <path
            d="M5 3.2h6.1L15 7.1v9.4a1.3 1.3 0 0 1-1.3 1.3H5a1.3 1.3 0 0 1-1.3-1.3V4.5A1.3 1.3 0 0 1 5 3.2Z"
            fill="currentColor"
            opacity="0.1"
          />
          <path
            d="M5 3.2h6.1L15 7.1v9.4a1.3 1.3 0 0 1-1.3 1.3H5a1.3 1.3 0 0 1-1.3-1.3V4.5A1.3 1.3 0 0 1 5 3.2Z"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linejoin="round"
          />
          <path d="M11 3.4v3.9h3.8" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
        </svg>
      </span>

      <button
        v-if="node.kind === 'dir'"
        class="card-name card-name-button"
        type="button"
        :title="node.name"
        @click.stop="emit('open', node)"
      >
        {{ node.name }}
      </button>
      <span v-else class="card-name" :title="node.name">{{ node.name }}</span>
      <span class="card-size numeric">{{ sizeLabel(node) }}</span>
    </li>
  </ul>
</template>

<style scoped>
.file-grid {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.card {
  position: relative;
  display: flex;
  min-height: 156px;
  flex-direction: column;
  gap: 9px;
  padding: 12px 13px 13px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  transition:
    background-color var(--dur) var(--ease),
    border-color var(--dur) var(--ease);
}

.card:hover {
  border-color: var(--color-border-strong);
}

.card.is-selected {
  background: var(--color-primary-soft);
  border-color: var(--color-primary);
  box-shadow: 0 0 0 1px var(--color-primary), 0 6px 16px rgba(249, 115, 22, 0.12);
}

.card.is-dir {
  cursor: pointer;
}

/* 44x44 touch target; the visible box stays 18x18 at (9,9) inside the card. */
.card-check-hit {
  position: absolute;
  top: -4px;
  left: -4px;
  display: grid;
  width: 44px;
  height: 44px;
  margin: 0;
  padding: 0;
  place-items: center;
  border: 0;
  border-radius: 50%;
  background: none;
  cursor: pointer;
  opacity: 0;
  user-select: none;
  transition: opacity var(--dur) var(--ease);
}

.card-check-box {
  display: inline-flex;
  width: 18px;
  height: 18px;
  align-items: center;
  justify-content: center;
  border: 1.5px solid var(--color-border-strong);
  border-radius: 5px;
  background: var(--color-bg);
  color: transparent;
  transition:
    background-color var(--dur) var(--ease),
    border-color var(--dur) var(--ease),
    color var(--dur) var(--ease);
}

.card-check-hit:hover .card-check-box {
  border-color: var(--color-primary);
}

.card-check-box svg {
  width: 11px;
  height: 11px;
  opacity: 0;
  transform: scale(0.6);
  transition:
    opacity var(--dur) var(--ease),
    transform var(--dur) var(--ease);
}

.card:hover .card-check-hit,
.card-check-hit:focus-visible,
.card-check-hit[aria-checked='true'] {
  opacity: 1;
}

.card-check-hit:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -4px;
}

.card-check-hit[aria-checked='true'] .card-check-box {
  border-color: var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-primary);
}

.card-check-hit[aria-checked='true'] .card-check-box svg {
  opacity: 1;
  transform: scale(1);
}

.card-menu {
  position: absolute;
  top: 6px;
  right: 6px;
  opacity: 0;
  transition: opacity var(--dur) var(--ease);
}

.card:hover .card-menu,
.card-menu:focus-within {
  opacity: 1;
}

.card-menu :deep(.row-menu-button) {
  width: 26px;
  height: 26px;
}

.card-icon {
  display: block;
  margin-top: 14px;
  color: var(--color-primary);
}

.card-icon.is-file {
  color: var(--color-muted);
}

.card-name {
  display: -webkit-box;
  overflow: hidden;
  font-size: var(--fs-base);
  line-height: 1.45;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.card-size {
  margin-top: auto;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

/* Folder names are the primary way into a directory, so they are real buttons. */
.card-name-button {
  width: 100%;
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.card-name-button:hover {
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

@media (max-width: 1199px) {
  .file-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

@media (max-width: 899px) {
  .file-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 599px) {
  .file-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 700px) {
  .card {
    min-height: 140px;
    padding: 10px 11px 11px;
  }

  /* Touch layouts have no hover, so keep the controls visible. */
  .card-check-hit,
  .card-menu {
    opacity: 1;
  }
}
</style>
