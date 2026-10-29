<script setup lang="ts">
import { computed } from 'vue'
import type { NodeItem } from '../../api/client'
import { formatBytes, formatDateTime } from '../../lib/format'
import { fileStem } from '../../lib/media'

const props = defineProps<{
  node: NodeItem
  parentId: number
  downloading?: boolean
}>()
const emit = defineEmits<{ download: [] }>()

const title = computed(() => fileStem(props.node.name))
</script>

<template>
  <div class="video-info">
    <div class="info-main">
      <h1 class="info-title">{{ title }}</h1>
      <p class="info-meta">
        <span>{{ formatBytes(node.size) }}</span>
        <span class="dot" aria-hidden="true">·</span>
        <span>{{ formatDateTime(node.updated_at) }}</span>
      </p>
    </div>
    <div class="info-actions">
      <button type="button" class="btn-ghost" :disabled="downloading" @click="emit('download')">
        {{ downloading ? '下载中…' : '下载' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.video-info {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.info-title {
  margin: 0;
  font-size: 18px;
  line-height: 1.35;
  color: var(--color-text);
  word-break: break-word;
}

.info-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--color-muted);
}

.dot {
  opacity: 0.6;
}

.info-actions {
  display: flex;
  gap: 8px;
  flex: none;
}

.btn-ghost {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 12px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-surface);
  color: var(--color-text);
  font-size: 13px;
  text-decoration: none;
  cursor: pointer;
}

.btn-ghost:hover {
  border-color: var(--color-border-strong);
  background: var(--color-primary-soft);
}

.btn-ghost:disabled {
  opacity: 0.6;
  cursor: default;
}

@media (max-width: 700px) {
  .video-info {
    flex-direction: column;
  }
}
</style>
