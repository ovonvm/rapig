<script setup lang="ts">
import { computed } from 'vue'
import { formatBytes } from '../lib/format'
import { statusLabel, type DownloadTask } from '../stores/downloads'

const props = withDefaults(defineProps<{ task: DownloadTask; wide?: boolean }>(), { wide: false })
const emit = defineEmits<{ pause: [id: number]; resume: [id: number]; cancel: [id: number] }>()

const percent = computed(() => {
  const total = props.task.total || props.task.size
  if (total <= 0) return 0
  return Math.min(100, Math.round((props.task.received / total) * 100))
})

const meta = computed(() => {
  if (props.task.status === 'downloading') {
    return `${formatBytes(props.task.received)} / ${formatBytes(props.task.size)}`
  }
  if (props.task.status === 'done') return formatBytes(props.task.size)
  if (props.task.status === 'failed') return props.task.error ? `失败 · ${props.task.error}` : '下载失败'
  return formatBytes(props.task.size)
})

const tone = computed(() => {
  if (props.task.status === 'done') return 'is-done'
  if (props.task.status === 'failed') return 'is-failed'
  if (props.task.status === 'paused') return 'is-paused'
  return 'is-active'
})
</script>

<template>
  <li class="download-row" :class="[tone, { 'is-wide': wide }]">
    <span class="row-icon" aria-hidden="true">
      <svg width="18" height="18" viewBox="0 0 20 20">
        <path
          d="M5 2.9h6.2L15.4 7v9.1a1.2 1.2 0 0 1-1.2 1.2H5a1.2 1.2 0 0 1-1.2-1.2V4.1A1.2 1.2 0 0 1 5 2.9Z"
          fill="none"
          stroke="currentColor"
          stroke-width="1.4"
          stroke-linejoin="round"
        />
        <path d="M11.2 3v4.1h4.2" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
      </svg>
    </span>

    <div class="row-main">
      <span class="row-name" :title="task.name">{{ task.name }}</span>
      <span class="row-meta">
        <span class="row-status">{{ statusLabel(task.status) }}</span>
        <span class="row-size numeric">{{ meta }}</span>
      </span>
      <span
        v-if="task.status === 'downloading' || task.status === 'paused' || task.status === 'queued'"
        class="row-track"
        aria-hidden="true"
      >
        <span class="row-fill" :style="{ width: `${percent}%` }"></span>
      </span>
    </div>

    <div class="row-actions">
      <button
        v-if="task.status === 'downloading'"
        class="row-icon-button"
        type="button"
        :aria-label="`暂停 ${task.name}`"
        @click="emit('pause', task.id)"
      >
        <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
          <path d="M4 2.6v6.8M8 2.6v6.8" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" />
        </svg>
      </button>
      <button
        v-else-if="task.status === 'paused' || task.status === 'failed'"
        class="row-icon-button"
        type="button"
        :aria-label="`${task.status === 'failed' ? '重试' : '继续'} ${task.name}`"
        @click="emit('resume', task.id)"
      >
        <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
          <path d="M4 2.7 9.2 6 4 9.3V2.7Z" fill="currentColor" />
        </svg>
      </button>
      <button
        v-if="task.status !== 'done'"
        class="row-icon-button"
        type="button"
        :aria-label="`取消 ${task.name}`"
        @click="emit('cancel', task.id)"
      >
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <path d="m2.2 2.2 5.6 5.6M7.8 2.2 2.2 7.8" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
        </svg>
      </button>
    </div>
  </li>
</template>

<style scoped>
.download-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 12px;
  border-top: 1px solid var(--color-border);
}

.download-row:first-child {
  border-top: 0;
}

.row-icon {
  display: grid;
  flex: none;
  place-items: center;
  width: 30px;
  height: 30px;
  border-radius: var(--radius-sm);
  background: var(--color-bg);
  color: var(--color-muted);
}

.is-done .row-icon {
  color: var(--color-success);
}

.is-failed .row-icon {
  color: var(--color-danger);
}

.row-main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.row-name {
  overflow: hidden;
  font-size: var(--fs-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.row-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.row-status {
  flex: none;
}

.is-active .row-status {
  color: var(--color-primary-ink);
}

.is-done .row-status {
  color: var(--color-success);
}

.is-failed .row-status {
  color: var(--color-danger);
}

.row-size {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.row-track {
  display: block;
  height: 3px;
  margin-top: 2px;
  overflow: hidden;
  border-radius: var(--radius-pill);
  background: var(--color-border);
}

.row-fill {
  display: block;
  height: 100%;
  background: var(--color-primary);
  transition: width 200ms var(--ease);
}

.row-actions {
  display: flex;
  flex: none;
  gap: 2px;
  align-items: center;
}

.row-icon-button {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 6px;
  color: var(--color-muted);
}

.row-icon-button:hover {
  background: var(--color-bg);
  color: var(--color-text);
}

.is-wide {
  padding: 12px 16px;
}
</style>
