<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import BatchBar from '../components/BatchBar.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import SkeletonBlock from '../components/SkeletonBlock.vue'
import type { BatchAction } from '../components/BatchBar.vue'
import { useTrashStore } from '../stores/trash'
import { useToast } from '../composables/useToast'
import { formatDateTime } from '../lib/format'
import { messageForError } from '../lib/errors'
import { runWithConcurrency } from '../lib/concurrency'
import { useAuthStore } from '../stores/auth'
import type { TrashItem } from '../api/client'

const BATCH_CONCURRENCY = 3

const trash = useTrashStore()
const toast = useToast()
const auth = useAuthStore()
const router = useRouter()

const emptyOpen = ref(false)
const emptying = ref(false)
const emptyError = ref<string | null>(null)
const busyId = ref<number | null>(null)
const batchBusy = ref(false)
const selectedIds = ref<number[]>([])

const selectedSet = computed(() => new Set(selectedIds.value))
const selectedItems = computed(() => trash.items.filter((item) => selectedSet.value.has(item.id)))
const allSelected = computed(
  () => trash.items.length > 0 && selectedIds.value.length === trash.items.length,
)

const summary = computed(() => {
  if (trash.loading || trash.error || trash.total === 0) return ''
  return `共 ${trash.total} 项，超过保留期后自动彻底删除`
})

const batchActions = computed<BatchAction[]>(() => [
  { key: 'restore', label: '恢复', icon: 'restore' },
  { key: 'purge', label: '彻底删除', icon: 'purge', danger: true },
])

function originalLabel(item: TrashItem): string {
  return item.original_parent_id === 0 ? '我的文件' : '…'
}

function remainingLabel(item: TrashItem): string {
  return item.remaining_days === 0 ? '不足 1 天' : `${item.remaining_days} 天`
}

function clearSelection(): void {
  selectedIds.value = []
}

function selectAll(): void {
  selectedIds.value = trash.items.map((item) => item.id)
}

function onSelect(id: number): void {
  const next = selectedIds.value.filter((value) => value !== id)
  if (next.length === selectedIds.value.length) next.push(id)
  selectedIds.value = next
}

async function refresh(): Promise<void> {
  await trash.load()
  clearSelection()
  if (trash.errorCode === 'unauthenticated') {
    auth.clearUser()
    toast.push('登录状态已失效，请重新登录', 'error')
    await router.replace({ name: 'login' })
  }
}

async function restore(item: TrashItem): Promise<void> {
  if (busyId.value !== null) return
  busyId.value = item.id
  try {
    const node = await trash.restore(item.id)
    void trash.refreshTotal()
    toast.push(`已恢复「${node.name}」`)
  } catch (cause) {
    toast.push(messageForError(cause, '恢复失败，请稍后重试'), 'error')
    await refresh()
  } finally {
    busyId.value = null
  }
}

async function purge(item: TrashItem): Promise<void> {
  if (busyId.value !== null) return
  busyId.value = item.id
  try {
    await trash.purge(item.id)
    void trash.refreshTotal()
    toast.push(`已彻底删除「${item.name}」`)
  } catch (cause) {
    toast.push(messageForError(cause, '删除失败，请稍后重试'), 'error')
    await refresh()
  } finally {
    busyId.value = null
  }
}

async function onBatchAction(key: string): Promise<void> {
  if (batchBusy.value || selectedItems.value.length === 0) return
  const targets = selectedItems.value.slice()
  batchBusy.value = true

  try {
    if (key === 'restore') {
      const result = await runWithConcurrency(targets, BATCH_CONCURRENCY, (item) => trash.restore(item.id))
      clearSelection()
      void trash.refreshTotal()
      const firstFailure = result.failures[0]
      toast.push(
        firstFailure
          ? `已恢复 ${result.succeeded} 项，失败 ${result.failures.length} 项：${messageForError(firstFailure.error, '未知错误')}`
          : `已恢复 ${result.succeeded} 项`,
        firstFailure ? 'error' : 'success',
      )
    } else if (key === 'purge') {
      const result = await runWithConcurrency(targets, BATCH_CONCURRENCY, (item) => trash.purge(item.id))
      clearSelection()
      void trash.refreshTotal()
      const firstFailure = result.failures[0]
      toast.push(
        firstFailure
          ? `已彻底删除 ${result.succeeded} 项，失败 ${result.failures.length} 项：${messageForError(firstFailure.error, '未知错误')}`
          : `已彻底删除 ${result.succeeded} 项`,
        firstFailure ? 'error' : 'success',
      )
    }
  } finally {
    batchBusy.value = false
  }
}

function openEmpty(): void {
  emptyError.value = null
  emptyOpen.value = true
}

async function emptyAll(): Promise<void> {
  if (emptying.value) return
  emptying.value = true
  emptyError.value = null
  try {
    const purged = await trash.empty()
    emptyOpen.value = false
    clearSelection()
    void trash.refreshTotal()
    toast.push(purged > 0 ? `已彻底删除 ${purged} 项` : '回收站已经是空的')
  } catch (cause) {
    emptyError.value = messageForError(cause, '清空失败，请稍后重试')
  } finally {
    emptying.value = false
  }
}

onMounted(() => {
  void refresh()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">回收站</h1>
        <p v-if="summary" class="page-sub">{{ summary }}</p>
      </div>

      <div class="page-actions">
        <button
          class="btn btn-secondary"
          type="button"
          :disabled="trash.total === 0 || trash.loading || emptying"
          @click="openEmpty"
        >
          清空回收站
        </button>
      </div>
    </header>

    <section class="panel" aria-label="回收站列表">
      <div v-if="trash.loading" class="skeleton-list" aria-hidden="true">
        <div v-for="row in 3" :key="row" class="skeleton-row">
          <SkeletonBlock width="15px" height="15px" />
          <SkeletonBlock :width="`${40 + row * 8}%`" />
          <SkeletonBlock width="72px" />
          <SkeletonBlock width="92px" />
        </div>
        <p class="visually-hidden">加载中…</p>
      </div>

      <div v-else-if="trash.error" class="state" role="alert">
        <h2 class="state-title">加载失败</h2>
        <p class="state-desc">{{ trash.error }}</p>
        <button class="btn btn-secondary state-action" type="button" @click="refresh">重试</button>
      </div>

      <EmptyState
        v-else-if="trash.items.length === 0"
        icon="trash"
        title="回收站是空的"
        description="删除的文件会先放在这里，超过保留期后自动彻底删除。"
      />

      <template v-else>
        <div class="cols list-head" aria-hidden="true">
          <span class="col-check"></span>
          <span>名称</span>
          <span class="col-mid">原位置</span>
          <span class="col-mid">删除时间</span>
          <span class="col-mid">剩余</span>
          <span class="col-action"></span>
        </div>

        <ul class="rows">
          <li
            v-for="item in trash.items"
            :key="item.id"
            class="cols row"
            :class="{ 'is-selected': selectedSet.has(item.id) }"
          >
            <span class="col-check">
              <input
                class="row-check"
                type="checkbox"
                :checked="selectedSet.has(item.id)"
                :aria-label="`选择 ${item.name}`"
                @change="onSelect(item.id)"
              />
            </span>
            <span class="cell-name">
              <svg width="18" height="18" viewBox="0 0 20 20" aria-hidden="true">
                <path
                  d="M2.75 6.25A1.75 1.75 0 0 1 4.5 4.5h3.2l1.55 1.75h6.25a1.75 1.75 0 0 1 1.75 1.75v6.25a1.75 1.75 0 0 1-1.75 1.75H4.5a1.75 1.75 0 0 1-1.75-1.75V6.25Z"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linejoin="round"
                />
              </svg>
              <span class="name" :title="item.name">{{ item.name }}</span>
            </span>
            <span class="cell-meta col-mid">{{ originalLabel(item) }}</span>
            <span class="cell-meta col-mid numeric">{{ formatDateTime(item.trashed_at) }}</span>
            <span class="cell-meta col-mid">{{ remainingLabel(item) }}</span>
            <span class="col-action">
              <button class="row-button" type="button" :disabled="busyId !== null" @click="restore(item)">
                恢复
              </button>
              <button
                class="row-button is-danger"
                type="button"
                :disabled="busyId !== null"
                @click="purge(item)"
              >
                彻底删除
              </button>
            </span>
          </li>
        </ul>
      </template>
    </section>

    <BatchBar
      :count="selectedIds.length"
      :total="trash.items.length"
      :actions="batchActions"
      :all-selected="allSelected"
      @action="onBatchAction"
      @clear="clearSelection"
      @select-all="selectAll"
    />

    <ConfirmDialog
      :open="emptyOpen"
      title="清空回收站？"
      message="回收站中的所有内容都会被彻底删除，无法恢复。"
      detail="这个操作会立即释放对应的存储空间。"
      confirm-label="清空回收站"
      require-text="清空"
      danger
      :busy="emptying"
      :error="emptyError"
      @close="emptyOpen = false"
      @confirm="emptyAll"
    />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.page-title {
  margin: 0;
  font-size: var(--fs-xl);
}

.page-sub {
  margin: 4px 0 0;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.page-actions {
  flex: none;
}

.panel {
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.cols {
  display: grid;
  grid-template-columns: 26px minmax(0, 1fr) 110px 148px 84px 156px;
  column-gap: 16px;
  align-items: center;
}

.col-check {
  display: flex;
  align-items: center;
}

.row-check {
  width: 15px;
  height: 15px;
  margin: 0;
  accent-color: var(--color-primary);
  cursor: pointer;
}

.list-head {
  padding: 10px 16px;
  background: var(--color-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.col-mid {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.col-action {
  text-align: right;
}

.rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

.row {
  min-height: 50px;
  padding: 6px 16px;
  border-top: 1px solid var(--color-border);
}

.row:first-child {
  border-top: 0;
}

.row:hover {
  background: var(--color-primary-soft);
}

.row.is-selected {
  background: var(--color-primary-soft);
}

.cell-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  color: var(--color-muted);
}

.name {
  overflow: hidden;
  color: var(--color-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cell-meta {
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.row-button {
  margin-left: 8px;
  padding: 4px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font-size: var(--fs-xs);
  color: var(--color-text);
  background: var(--color-surface);
}

.row-button:hover:not(:disabled) {
  border-color: var(--color-primary);
  color: var(--color-primary-ink);
}

.row-button.is-danger:hover:not(:disabled) {
  border-color: var(--color-danger);
  color: var(--color-danger);
}

.row-button:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.skeleton-list {
  display: flex;
  flex-direction: column;
}

.skeleton-row {
  display: flex;
  gap: 14px;
  align-items: center;
  padding: 14px 16px;
  border-top: 1px solid var(--color-border);
}

.skeleton-row:first-child {
  border-top: 0;
}

.state {
  padding: 48px 24px;
  text-align: center;
}

.state-title {
  margin: 0;
  font-size: var(--fs-lg);
}

.state-desc {
  margin: 8px 0 0;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.state-action {
  margin-top: 14px;
}

@media (max-width: 900px) {
  .cols {
    grid-template-columns: 24px minmax(0, 1fr) 96px 120px 150px;
  }

  .col-trashed {
    display: none;
  }
}

@media (max-width: 700px) {
  .cols {
    grid-template-columns: 24px minmax(0, 1fr) 84px 128px;
    column-gap: 12px;
  }

  .col-remaining,
  .col-trashed {
    display: none;
  }
}
</style>
