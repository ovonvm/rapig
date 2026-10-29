<script setup lang="ts">
export type BatchIcon = 'download' | 'share' | 'move' | 'trash' | 'restore' | 'purge'

export interface BatchAction {
  key: string
  label: string
  icon?: BatchIcon
  danger?: boolean
  disabled?: boolean
  title?: string
}

withDefaults(
  defineProps<{
    count: number
    total: number
    actions: BatchAction[]
    allSelected?: boolean
  }>(),
  { allSelected: false },
)

const emit = defineEmits<{ action: [key: string]; clear: []; selectAll: [] }>()

/** One path per icon keeps the bar free of a second icon component. */
const ICONS: Record<BatchIcon, string> = {
  download: 'M7 2v7.1M4.2 6.4 7 9.2l2.8-2.8M2.6 10.6v.5a1.1 1.1 0 0 0 1.1 1.1h6.6a1.1 1.1 0 0 0 1.1-1.1v-.5',
  share:
    'M5.8 8.2 8.2 5.8M4.6 9.4l-1 1a2 2 0 0 0 2.8 2.8l1-1M9.4 4.6l1-1a2 2 0 0 1 2.8 2.8l-1 1',
  move:
    'M2.6 6.5a1.5 1.5 0 0 1 1.5-1.5h2.6l1.3 1.5h2.9a1.5 1.5 0 0 1 1.5 1.5v3.2a1.5 1.5 0 0 1-1.5 1.5H4.1a1.5 1.5 0 0 1-1.5-1.5V6.5Z',
  trash: 'M3.2 4.4h7.6M5.7 4.4V3.2h2.6v1.2M4.4 4.4l.5 6.4h4.2l.5-6.4',
  restore: 'M3.4 7.2a3.7 3.7 0 1 1 1.1 2.6M3.2 3.2v2.9h2.9',
  purge: 'm4 4 6 6M10 4l-6 6',
}
</script>

<template>
  <Teleport to="body">
    <Transition name="bar">
      <div v-if="count > 0" class="batch-bar" role="region" aria-label="批量操作">
        <div class="batch-info">
          <span class="batch-count" aria-live="polite">已选 {{ count }} 项</span>
          <button class="batch-link" type="button" :disabled="allSelected || count >= total" @click="emit('selectAll')">
            全选
          </button>
          <button class="batch-link" type="button" @click="emit('clear')">取消选择</button>
        </div>

        <div class="batch-actions">
          <button
            v-for="action in actions"
            :key="action.key"
            class="batch-button"
            :class="{ 'is-danger': action.danger }"
            type="button"
            :disabled="action.disabled"
            :title="action.title ?? action.label"
            @click="emit('action', action.key)"
          >
            <svg
              v-if="action.icon"
              width="14"
              height="14"
              viewBox="0 0 14 14"
              aria-hidden="true"
              fill="none"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <path :d="ICONS[action.icon]" />
            </svg>
            {{ action.label }}
          </button>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.batch-bar {
  position: fixed;
  bottom: calc(18px + env(safe-area-inset-bottom, 0px));
  left: 50%;
  z-index: 45;
  display: flex;
  gap: 16px;
  align-items: center;
  max-width: calc(100vw - 24px);
  padding: 9px 12px 9px 16px;
  background: color-mix(in srgb, var(--color-surface) 88%, transparent);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  backdrop-filter: blur(10px);
  transform: translateX(-50%);
}

.batch-info {
  display: flex;
  gap: 12px;
  align-items: center;
  white-space: nowrap;
}

.batch-count {
  font-size: var(--fs-sm);
  font-weight: 500;
}

.batch-link {
  padding: 0;
  border: 0;
  background: none;
  font-size: var(--fs-sm);
  color: var(--color-primary-ink);
  cursor: pointer;
}

.batch-link:hover:not(:disabled) {
  text-decoration: underline;
  text-underline-offset: 2px;
}

.batch-link:disabled {
  color: var(--color-muted);
  opacity: 0.6;
  cursor: not-allowed;
}

.batch-actions {
  display: flex;
  gap: 6px;
  align-items: center;
}

.batch-button {
  display: inline-flex;
  gap: 5px;
  align-items: center;
  height: 30px;
  padding: 0 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  font-size: var(--fs-sm);
  color: var(--color-text);
  cursor: pointer;
}

.batch-button:hover:not(:disabled) {
  border-color: var(--color-primary);
  color: var(--color-primary-ink);
}

.batch-button.is-danger {
  color: var(--color-danger);
}

.batch-button.is-danger:hover:not(:disabled) {
  border-color: var(--color-danger);
  background: var(--color-danger-soft);
}

.batch-button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.bar-enter-active,
.bar-leave-active {
  transition:
    opacity 160ms var(--ease),
    transform 160ms var(--ease);
}

.bar-enter-from,
.bar-leave-to {
  opacity: 0;
  transform: translate(-50%, 8px);
}

@media (max-width: 640px) {
  .batch-bar {
    right: 12px;
    left: 12px;
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
    transform: none;
  }

  .batch-info {
    justify-content: flex-start;
  }

  .batch-actions {
    overflow-x: auto;
  }

  .batch-button {
    flex: 1;
    justify-content: center;
    white-space: nowrap;
  }

  .bar-enter-from,
  .bar-leave-to {
    transform: translateY(8px);
  }
}
</style>
