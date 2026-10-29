<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useFoldersStore } from '../stores/folders'

const props = withDefaults(
  defineProps<{
    id: number
    name: string
    depth: number
    selectedId: number
    movingId: number
    defaultExpanded?: boolean
  }>(),
  { defaultExpanded: false },
)

const emit = defineEmits<{ select: [id: number, name: string] }>()

const folders = useFoldersStore()
const expanded = ref(props.defaultExpanded)

function expand(): void {
  expanded.value = true
  void folders.loadChildren(props.id)
}

function toggle(): void {
  if (expanded.value) {
    expanded.value = false
    return
  }
  expand()
}

function retry(): void {
  void folders.loadChildren(props.id)
}

function select(): void {
  if (props.id === props.movingId) return
  emit('select', props.id, props.name)
}

function onChildSelect(id: number, name: string): void {
  emit('select', id, name)
}

onMounted(() => {
  if (expanded.value) void folders.loadChildren(props.id)
})
</script>

<template>
  <li class="tree-node">
    <div
      class="tree-row"
      :class="{ 'is-selected': id === selectedId, 'is-disabled': id === movingId }"
      :style="{ paddingLeft: `${depth * 16 + 6}px` }"
    >
      <button
        class="tree-toggle"
        type="button"
        :aria-label="expanded ? `收起「${name}」` : `展开「${name}」`"
        :aria-expanded="expanded"
        @click.stop="toggle"
      >
        <svg
          class="chevron"
          :class="{ 'is-open': expanded }"
          width="12"
          height="12"
          viewBox="0 0 12 12"
          aria-hidden="true"
        >
          <path
            d="M4.5 2.5 8 6l-3.5 3.5"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>

      <button
        class="tree-label"
        type="button"
        :disabled="id === movingId"
        :aria-pressed="id === selectedId"
        @click="select"
      >
        <svg class="tree-folder" width="16" height="16" viewBox="0 0 20 20" aria-hidden="true">
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
        <span class="tree-name" :title="name">{{ name }}</span>
      </button>
    </div>

    <div v-if="expanded" class="tree-children">
      <p v-if="folders.isLoading(id)" class="tree-note">加载中…</p>
      <template v-else-if="folders.errorOf(id)">
        <p class="tree-note is-error">{{ folders.errorOf(id) }}</p>
        <button class="tree-retry" type="button" @click="retry">重试</button>
      </template>
      <p v-else-if="folders.childrenOf(id).length === 0" class="tree-note">没有子文件夹</p>
      <ul v-else class="tree-list">
        <DirTreeNode
          v-for="child in folders.childrenOf(id)"
          :key="child.id"
          :id="child.id"
          :name="child.name"
          :depth="depth + 1"
          :selected-id="selectedId"
          :moving-id="movingId"
          @select="onChildSelect"
        />
      </ul>
    </div>
  </li>
</template>

<style scoped>
.tree-node {
  list-style: none;
}

.tree-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.tree-row {
  display: flex;
  align-items: center;
  gap: 2px;
  min-height: 32px;
  padding-right: 8px;
  border-radius: var(--radius-sm);
}

.tree-row:hover {
  background: var(--color-bg);
}

.tree-row.is-selected {
  background: var(--color-primary-soft);
}

.tree-toggle {
  display: grid;
  flex: none;
  width: 22px;
  height: 26px;
  place-items: center;
  border-radius: 5px;
  color: var(--color-muted);
}

.tree-toggle:hover {
  color: var(--color-text);
}

.chevron {
  transition: transform var(--dur) var(--ease);
}

.chevron.is-open {
  transform: rotate(90deg);
}

.tree-label {
  display: flex;
  flex: 1;
  min-width: 0;
  align-items: center;
  gap: 8px;
  height: 32px;
  border-radius: var(--radius-sm);
  color: var(--color-text);
  font-size: var(--fs-sm);
  text-align: left;
}

.tree-label:disabled {
  color: var(--color-muted);
  cursor: not-allowed;
}

.tree-label:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}

.tree-folder {
  flex: none;
  color: var(--color-primary);
}

.tree-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-note {
  padding: 4px 10px 6px 28px;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.tree-note.is-error {
  color: var(--color-danger);
}

.tree-retry {
  margin: 0 0 6px 40px;
  font-size: var(--fs-xs);
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}
</style>
