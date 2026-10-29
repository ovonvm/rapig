<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import DirTreeNode from './DirTreeNode.vue'
import { useFoldersStore } from '../stores/folders'
import type { NodeItem } from '../api/client'

/**
 * Pure folder picker: it never calls the API itself. The owner runs the move
 * (single row or batch) and feeds `busy` / `error` / `stale` back in, so the
 * single-row path keeps the same feedback it had before batch support.
 */
const props = withDefaults(
  defineProps<{
    open: boolean
    nodes: NodeItem[]
    /** Legacy single-node prop, kept so old call sites keep working. */
    node?: NodeItem | null
    busy?: boolean
    error?: string | null
    stale?: boolean
    /** Tree root: 0 for the personal drive, `group.root_id` for a group (P1). */
    rootId?: number
    rootName?: string
  }>(),
  { node: null, busy: false, error: null, stale: false, rootId: 0, rootName: '我的文件' },
)

const emit = defineEmits<{
  close: []
  moveTo: [parent: number]
  refresh: []
}>()

const folders = useFoldersStore()

const dialogRef = ref<HTMLElement | null>(null)
const selectedId = ref(0)

const targets = computed<NodeItem[]>(() => {
  if (props.nodes.length > 0) return props.nodes
  return props.node ? [props.node] : []
})

const single = computed<NodeItem | null>(() => (targets.value.length === 1 ? targets.value[0] ?? null : null))

const subtitle = computed(() => {
  if (targets.value.length === 0) return '选择目标文件夹'
  if (targets.value.length === 1) return `把「${targets.value[0]?.name ?? ''}」移动到：`
  return `把 ${targets.value.length} 项移动到：`
})

/** The row that cannot be picked as its own new parent. */
const movingId = computed(() => single.value?.id ?? -1)

const alreadyThere = computed(
  () => targets.value.length > 0 && targets.value.every((node) => node.parent_id === selectedId.value),
)

const invalidTarget = computed(() => targets.value.some((node) => node.id === selectedId.value))

const disabled = computed(
  () => props.busy || targets.value.length === 0 || alreadyThere.value || invalidTarget.value,
)

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      document.body.style.overflow = ''
      return
    }

    selectedId.value = single.value?.parent_id ?? props.rootId
    folders.reset()
    document.body.style.overflow = 'hidden'
    await nextTick()
    void folders.loadChildren(props.rootId)
    dialogRef.value?.querySelector<HTMLElement>('button')?.focus()
  },
)

onBeforeUnmount(() => {
  document.body.style.overflow = ''
})

function onSelect(id: number): void {
  if (props.busy) return
  selectedId.value = id
}

function submit(): void {
  if (disabled.value) return
  emit('moveTo', selectedId.value)
}

function close(): void {
  if (props.busy) return
  emit('close')
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Escape') return
  event.stopPropagation()
  close()
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="overlay" @mousedown.self="close" @keydown="onKeydown">
      <div ref="dialogRef" class="dialog" role="dialog" aria-modal="true" aria-labelledby="move-dialog-title">
        <h2 id="move-dialog-title" class="dialog-title">移动</h2>
        <p class="dialog-sub">{{ subtitle }}</p>

        <div class="tree-panel" role="tree" aria-label="目标文件夹">
          <DirTreeNode
            :id="rootId"
            :name="rootName"
            :depth="0"
            :selected-id="selectedId"
            :moving-id="movingId"
            :default-expanded="true"
            @select="onSelect"
          />
        </div>

        <p v-if="alreadyThere" class="dialog-hint">已经在该位置，请选择其它文件夹。</p>
        <p v-else-if="invalidTarget" class="dialog-hint is-error">不能移动到所选的文件夹自身。</p>
        <p v-if="error" class="dialog-error" role="alert">{{ error }}</p>
        <button v-if="stale" class="link-button" type="button" @click="emit('refresh')">
          列表已过期，刷新后重试
        </button>

        <div class="dialog-actions">
          <button class="btn btn-secondary" type="button" :disabled="busy" @click="close">取消</button>
          <button class="btn btn-primary" type="button" :disabled="disabled" @click="submit">
            {{ busy ? '移动中…' : '移到这里' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: grid;
  place-items: center;
  padding: 24px;
  background: var(--color-overlay);
}

.dialog {
  width: min(420px, 100%);
  padding: 22px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.dialog-title {
  margin: 0 0 4px;
  font-size: var(--fs-lg);
}

.dialog-sub {
  margin: 0 0 14px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  overflow-wrap: anywhere;
}

.tree-panel {
  max-height: 260px;
  padding: 6px;
  overflow: auto;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.dialog-hint {
  margin: 8px 0 0;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.dialog-hint.is-error {
  color: var(--color-danger);
}

.dialog-error {
  margin: 10px 0 0;
  font-size: var(--fs-xs);
  color: var(--color-danger);
}

.link-button {
  margin-top: 6px;
  font-size: var(--fs-xs);
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 18px;
}
</style>
