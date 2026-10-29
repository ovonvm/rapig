<script setup lang="ts">
import { formatBytes } from '../lib/format'
import { useUploadsStore } from '../stores/uploads'
import type { UploadTask, UploadStatus } from '../stores/uploads'

const emit = defineEmits<{ resume: [id: number] }>()

const uploads = useUploadsStore()

const STATUS_LABELS: Record<UploadStatus, string> = {
  pending: '待续传',
  queued: '排队中',
  hashing: '准备中…',
  uploading: '上传中',
  completing: '合并中…',
  done: '已完成',
  deduped: '已秒传',
  canceled: '已取消',
  error: '上传失败',
}

function statusText(task: UploadTask): string {
  if (task.status === 'uploading') return `${task.percent}%`
  if (task.status === 'error') return task.error ?? STATUS_LABELS.error
  return STATUS_LABELS[task.status]
}

function partsText(task: UploadTask): string {
  if (task.partsTotal <= 0) return ''
  return `已完成 ${task.uploadedParts.size}/${task.partsTotal} 片`
}

function canResume(task: UploadTask): boolean {
  return task.status === 'pending'
}

function barPercent(task: UploadTask): number {
  if (task.status === 'done' || task.status === 'deduped' || task.status === 'completing') return 100
  if (task.status === 'canceled' || task.status === 'error') return task.percent
  return task.percent
}

function canCancel(task: UploadTask): boolean {
  return ['pending', 'queued', 'hashing', 'uploading', 'completing'].includes(task.status)
}

function canRetry(task: UploadTask): boolean {
  return task.status === 'error' || task.status === 'canceled'
}
</script>

<template>
  <section v-if="uploads.hasTasks" class="queue" aria-label="上传队列">
    <header class="queue-head">
      <h2 class="queue-title">
        上传队列
        <span v-if="uploads.activeCount > 0" class="queue-count">{{ uploads.activeCount }} 个进行中</span>
        <span v-else-if="uploads.pendingCount > 0" class="queue-count">{{ uploads.pendingCount }} 个待续传</span>
        <span v-else class="queue-count">已结束</span>
      </h2>
      <button v-if="uploads.hasFinished" class="queue-clear" type="button" @click="uploads.clearFinished()">
        清除已结束
      </button>
    </header>

    <ul class="queue-list">
      <li v-for="task in uploads.tasks" :key="task.id" class="item">
        <div class="item-main">
          <span class="item-name" :title="task.label">{{ task.label }}</span>
          <span class="item-meta">
            <span class="numeric">{{ formatBytes(task.size) }}</span>
            <span class="meta-dot" aria-hidden="true">·</span>
            <span v-if="task.status === 'pending'" class="item-parts">{{ partsText(task) }}</span>
            <span class="item-status" :class="`is-${task.status}`" :title="task.error ?? undefined">
              {{ statusText(task) }}
            </span>
          </span>
        </div>

        <div class="item-track" aria-hidden="true">
          <div
            class="item-fill"
            :class="[`is-${task.status}`, { 'is-pending': task.status === 'hashing' || task.status === 'queued' }]"
            :style="{ width: `${barPercent(task)}%` }"
          ></div>
        </div>

        <div class="item-actions">
          <button
            v-if="canResume(task)"
            class="item-resume"
            type="button"
            :aria-label="`继续上传 ${task.label}`"
            @click="emit('resume', task.id)"
          >
            继续上传
          </button>

          <button
            v-if="canRetry(task)"
            class="item-action"
            type="button"
            :aria-label="`重试上传 ${task.name}`"
            title="重试"
            @click="uploads.retry(task.id)"
          >
            <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
              <path
                d="M11.2 5.2A4.4 4.4 0 1 0 11.5 8"
                fill="none"
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
              />
              <path
                d="M11.7 2.4v3.1H8.6"
                fill="none"
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </button>

          <button
            v-if="canCancel(task)"
            class="item-action"
            type="button"
            :aria-label="`取消上传 ${task.name}`"
            title="取消"
            @click="uploads.cancel(task.id)"
          >
            <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
              <path d="m4 4 6 6M10 4l-6 6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
            </svg>
          </button>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.queue {
  margin-bottom: 16px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.queue-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--color-border);
}

.queue-title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: var(--fs-sm);
  font-weight: 600;
}

.queue-count {
  font-size: var(--fs-xs);
  font-weight: 400;
  color: var(--color-muted);
}

.queue-clear {
  font-size: var(--fs-xs);
  color: var(--color-primary-ink);
}

.queue-clear:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}

.queue-list {
  max-height: 264px;
  overflow-y: auto;
  overscroll-behavior: contain;
}

.item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  grid-template-areas:
    'main actions'
    'track actions';
  align-items: center;
  column-gap: 12px;
  row-gap: 5px;
  padding: 9px 14px;
  border-top: 1px solid var(--color-border);
}

.item:first-child {
  border-top: 0;
}

.item-main {
  grid-area: main;
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-width: 0;
}

.item-name {
  min-width: 0;
  overflow: hidden;
  font-size: var(--fs-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.item-meta {
  display: flex;
  flex: none;
  align-items: baseline;
  gap: 5px;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.meta-dot {
  opacity: 0.6;
}

.item-status.is-error {
  color: var(--color-danger);
}

.item-status.is-pending {
  color: var(--color-primary-ink);
  font-weight: 600;
}

.item-parts {
  color: var(--color-primary-ink);
}

.item-status.is-done,
.item-status.is-deduped {
  color: var(--color-success);
}

.item-status.is-deduped {
  font-weight: 600;
}

.item-track {
  grid-area: track;
  height: 3px;
  overflow: hidden;
  background: var(--color-border);
  border-radius: var(--radius-pill);
}

.item-fill {
  height: 100%;
  background: var(--color-primary);
  border-radius: inherit;
  transition: width 220ms var(--ease);
}

.item-fill.is-pending {
  animation: pending-slide 1.4s var(--ease) infinite;
}

.item-fill.is-done,
.item-fill.is-deduped {
  background: var(--color-success);
}

.item-fill.is-error {
  background: var(--color-danger);
}

.item-fill.is-canceled {
  background: var(--color-border-strong);
}

.item-actions {
  grid-area: actions;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 2px;
}

.item-resume {
  height: 24px;
  padding: 0 7px;
  font-size: var(--fs-xs);
  font-weight: 500;
  color: var(--color-primary-ink);
  border-radius: var(--radius-sm);
  transition: background-color var(--dur) var(--ease);
}

.item-resume:hover {
  background: var(--color-primary-soft);
}

.item-action {
  display: grid;
  width: 24px;
  height: 24px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
  transition:
    background-color var(--dur) var(--ease),
    color var(--dur) var(--ease);
}

.item-action:hover {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

@keyframes pending-slide {
  0%,
  100% {
    opacity: 0.45;
  }

  50% {
    opacity: 1;
  }
}

@media (max-width: 560px) {
  .item-main {
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
  }
}
</style>
