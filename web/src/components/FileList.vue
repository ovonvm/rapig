<script setup lang="ts">
import EmptyState from './EmptyState.vue'
import RowActions from './RowActions.vue'
import type { RowActionKind } from './RowActions.vue'
import { formatBytes, formatDateTime } from '../lib/format'
import { downloadUrl } from '../api/client'
import type { NodeItem } from '../api/client'

defineProps<{
  items: NodeItem[]
  loading: boolean
  error: string | null
  selectedIds: ReadonlySet<number>
  emptyTitle?: string
  emptyDescription?: string
  /** Restrict the row action menu; omitted means every default action. */
  actions?: RowActionKind[]
}>()

const emit = defineEmits<{
  retry: []
  create: []
  action: [kind: RowActionKind, node: NodeItem]
  /** Checkbox click: accumulates/toggles the selection. */
  toggle: [id: number, event: MouseEvent]
  /** Blank row click: plain single selection (folders open instead). */
  select: [node: NodeItem, event: MouseEvent]
  open: [node: NodeItem]
  /** Double click on a file row opens the preview overlay. */
  preview: [node: NodeItem]
}>()

function sizeLabel(node: NodeItem): string {
  return node.kind === 'dir' ? '—' : formatBytes(node.size)
}

/** A row click opens folders; files fall back to plain single selection. */
function onRowClick(node: NodeItem, event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('button, a, input, [role="menu"], [role="menuitem"]')) return
  if (node.kind === 'dir') {
    emit('open', node)
    return
  }
  emit('select', node, event)
}

/** Double click previews files; folders keep their single-click navigation. */
function onRowDblClick(node: NodeItem, event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('button, a, input, [role="menu"], [role="menuitem"]')) return
  if (node.kind !== 'file') return
  event.preventDefault()
  emit('preview', node)
}
</script>

<template>
  <section class="panel" aria-label="文件列表">
    <div v-if="loading || (items.length > 0 && !error)" class="cols list-head" aria-hidden="true">
      <span class="col-check"></span>
      <span>名称</span>
      <span class="col-size">大小</span>
      <span class="col-date">修改时间</span>
      <span class="col-action"></span>
    </div>

    <div v-if="loading" class="skeleton">
      <div v-for="row in 4" :key="row" class="cols skeleton-row">
        <span class="col-check"></span>
        <span class="skeleton-name">
          <span class="skeleton-block skeleton-icon"></span>
          <span class="skeleton-block" :style="{ width: `${40 + (row % 3) * 15}%` }"></span>
        </span>
        <span class="col-size">
          <span class="skeleton-block" style="width: 38px; margin-left: auto"></span>
        </span>
        <span class="col-date">
          <span class="skeleton-block" style="width: 104px; margin-left: auto"></span>
        </span>
        <span class="col-action"></span>
      </div>
      <p class="visually-hidden">加载中…</p>
    </div>

    <div v-else-if="error" class="state" role="alert">
      <h2 class="state-title">加载失败</h2>
      <p class="state-desc">{{ error }}</p>
      <button class="btn btn-secondary state-action" type="button" @click="emit('retry')">重试</button>
    </div>

    <EmptyState
      v-else-if="items.length === 0"
      :title="emptyTitle"
      :description="emptyDescription"
      @action="emit('create')"
    />

    <ul v-else class="rows">
      <li
        v-for="node in items"
        :key="node.id"
        class="cols row"
        :class="{ 'is-selected': selectedIds.has(node.id), 'is-dir': node.kind === 'dir' }"
        :data-node-id="node.id"
        @click="onRowClick(node, $event)"
        @dblclick="onRowDblClick(node, $event)"
      >
        <span class="col-check">
          <!-- The hit target is 44x44 for touch, while the box stays 18x18. -->
          <button
            type="button"
            class="row-check-hit"
            role="checkbox"
            :aria-checked="selectedIds.has(node.id) ? 'true' : 'false'"
            :aria-label="`选择 ${node.name}`"
            @click.prevent.stop="emit('toggle', node.id, $event)"
          >
            <span class="row-check-box">
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
        </span>
        <span class="cell-name">
          <svg
            v-if="node.kind === 'dir'"
            class="icon icon-dir"
            width="18"
            height="18"
            viewBox="0 0 20 20"
            aria-hidden="true"
          >
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
          <svg v-else class="icon icon-file" width="18" height="18" viewBox="0 0 20 20" aria-hidden="true">
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
          <button
            v-if="node.kind === 'dir'"
            class="name name-button"
            type="button"
            :title="node.name"
            @click.stop="emit('open', node)"
          >
            {{ node.name }}
          </button>
          <span v-else class="name" :title="node.name">{{ node.name }}</span>
        </span>
        <span class="cell-meta col-size numeric">{{ sizeLabel(node) }}</span>
        <span class="cell-meta col-date numeric">{{ formatDateTime(node.updated_at) }}</span>
        <span class="col-action">
          <a
            v-if="node.kind === 'file'"
            class="row-action"
            :href="downloadUrl(node.id)"
            :download="node.name"
            :aria-label="`下载 ${node.name}`"
            title="下载"
          >
            <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
              <path
                d="M8 2.4v7.1M5.2 7.2 8 10l2.8-2.8"
                fill="none"
                stroke="currentColor"
                stroke-width="1.4"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
              <path
                d="M3.1 11.4v1.1a1.1 1.1 0 0 0 1.1 1.1h7.6a1.1 1.1 0 0 0 1.1-1.1v-1.1"
                fill="none"
                stroke="currentColor"
                stroke-width="1.4"
                stroke-linecap="round"
              />
            </svg>
          </a>
          <RowActions :node="node" :actions="actions" @action="(kind, target) => emit('action', kind, target)" />
        </span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.panel {
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.cols {
  display: grid;
  grid-template-columns: 30px minmax(0, 1fr) 88px 156px 64px;
  column-gap: 16px;
  align-items: center;
}

.col-check {
  display: flex;
  align-items: center;
}

/* 44x44 touch target centred on the 30px checkbox column (box stays 18x18). */
.row-check-hit {
  display: grid;
  flex: none;
  width: 44px;
  height: 44px;
  margin-left: -7px;
  padding: 0;
  place-items: center;
  border: 0;
  border-radius: 50%;
  background: none;
  cursor: pointer;
  user-select: none;
}

.row-check-hit:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}

.row-check-box {
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

.row-check-hit:hover .row-check-box {
  border-color: var(--color-primary);
}

.row-check-box svg {
  width: 11px;
  height: 11px;
  opacity: 0;
  transform: scale(0.6);
  transition:
    opacity var(--dur) var(--ease),
    transform var(--dur) var(--ease);
}

.row-check-hit[aria-checked='true'] .row-check-box {
  border-color: var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-primary);
}

.row-check-hit[aria-checked='true'] .row-check-box svg {
  opacity: 1;
  transform: scale(1);
}

.list-head {
  padding: 10px 16px;
  background: var(--color-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.col-size,
.col-date {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

.row {
  min-height: 50px;
  padding: 0 16px;
  border-top: 1px solid var(--color-border);
  transition: background-color var(--dur) var(--ease);
}

.row:first-child {
  border-top: 0;
}

.row:hover {
  background: var(--color-primary-soft);
}

.row.is-selected {
  background: var(--color-primary-soft);
  box-shadow: inset 3px 0 0 var(--color-primary);
}

.cell-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.icon {
  flex: none;
}

.icon-dir {
  color: var(--color-primary);
}

.icon-file {
  color: var(--color-muted);
}

.name {
  overflow: hidden;
  font-size: var(--fs-base);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Folder names are the primary way into a directory, so they are real buttons. */
.name-button {
  min-width: 0;
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.name-button:hover {
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.row.is-dir {
  cursor: pointer;
}

.cell-meta {
  overflow: hidden;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.col-action {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 2px;
}

.row-action {
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

.row:hover .row-action,
.row-action:focus-visible {
  opacity: 1;
}

.row-action:hover {
  background: var(--color-surface);
  color: var(--color-primary-ink);
  opacity: 1;
}

.skeleton {
  padding: 0;
}

.skeleton-row {
  min-height: 50px;
  padding: 0 16px;
  border-top: 1px solid var(--color-border);
}

.skeleton-row:first-child {
  border-top: 0;
}

.skeleton-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.skeleton-block {
  display: inline-block;
  height: 10px;
  border-radius: var(--radius-pill);
  background: var(--color-border);
  animation: skeleton-pulse 1.5s var(--ease) infinite;
}

.skeleton-icon {
  flex: none;
  width: 18px;
  height: 18px;
  border-radius: 5px;
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 52px 24px 56px;
  text-align: center;
}

.state-title {
  font-size: 15px;
  font-weight: 600;
}

.state-desc {
  max-width: 360px;
  margin-top: 4px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.state-action {
  margin-top: 16px;
}

@keyframes skeleton-pulse {
  0%,
  100% {
    opacity: 0.55;
  }

  50% {
    opacity: 1;
  }
}

@media (max-width: 700px) {
  .cols {
    grid-template-columns: 26px minmax(0, 1fr) 72px 56px;
    column-gap: 12px;
  }

  .row-check-hit {
    margin-left: -9px;
  }

  .col-date {
    display: none;
  }

  .row-action {
    width: 24px;
    height: 24px;
  }
}
</style>
