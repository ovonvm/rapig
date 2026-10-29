<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { NodeItem } from '../../api/client'
import { fileStem, type MediaKind } from '../../lib/media'

const props = defineProps<{
  items: NodeItem[]
  currentId: number
  kind: MediaKind
}>()
const emit = defineEmits<{ select: [node: NodeItem] }>()

const listRef = ref<HTMLElement | null>(null)
const title = computed(() => (props.kind === 'audio' ? '播放列表' : '播放列表'))
const count = computed(() => props.items.length)

function pick(node: NodeItem): void {
  emit('select', node)
}

watch(
  () => props.currentId,
  async () => {
    await nextTick()
    const active = listRef.value?.querySelector<HTMLElement>('[data-active="true"]')
    active?.scrollIntoView({ block: 'nearest' })
  },
  { immediate: true },
)
</script>

<template>
  <aside class="playlist" aria-label="播放列表">
    <header class="playlist-head">
      <span class="playlist-title">{{ title }}</span>
      <span class="playlist-count">{{ count }} 项</span>
    </header>
    <ol ref="listRef" class="playlist-items">
      <li v-for="(node, index) in items" :key="node.id">
        <button
          type="button"
          class="playlist-item"
          :class="{ active: node.id === currentId }"
          :data-active="node.id === currentId ? 'true' : 'false'"
          :aria-current="node.id === currentId ? 'true' : undefined"
          @click="pick(node)"
        >
          <span class="playlist-index" aria-hidden="true">
            <svg v-if="node.id === currentId" viewBox="0 0 24 24" width="14" height="14">
              <path d="M8 5v14l11-7z" fill="currentColor" />
            </svg>
            <template v-else>{{ index + 1 }}</template>
          </span>
          <span class="playlist-name">{{ fileStem(node.name) }}</span>
          <span class="playlist-duration" aria-hidden="true">--:--</span>
        </button>
      </li>
    </ol>
    <p v-if="count === 0" class="playlist-empty">当前文件夹没有同类媒体文件</p>
  </aside>
</template>

<style scoped>
.playlist {
  display: flex;
  flex-direction: column;
  min-height: 0;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
  overflow: hidden;
}

.playlist-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: 14px 16px 10px;
  border-bottom: 1px solid var(--color-border);
}

.playlist-title {
  font-weight: 600;
  color: var(--color-text);
}

.playlist-count {
  font-size: 12px;
  color: var(--color-muted);
}

.playlist-items {
  margin: 0;
  padding: 6px;
  list-style: none;
  overflow-y: auto;
  min-height: 0;
}

.playlist-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text);
  text-align: left;
  cursor: pointer;
}

.playlist-item:hover {
  background: var(--color-primary-soft);
}

.playlist-item.active {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.playlist-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  flex: none;
  font-variant-numeric: tabular-nums;
  color: var(--color-muted);
}

.playlist-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.playlist-duration {
  flex: none;
  font-size: 12px;
  color: var(--color-muted);
}

.playlist-empty {
  margin: 0;
  padding: 16px;
  color: var(--color-muted);
  font-size: 13px;
}

@media (max-width: 900px) {
  .playlist {
    max-height: 260px;
  }
}
</style>
