<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import OfflineDownloadDialog from '../components/OfflineDownloadDialog.vue'
import SkeletonBlock from '../components/SkeletonBlock.vue'
import type { TransferItem, TransferState } from '../api/client'
import { messageForError } from '../lib/errors'
import { formatBytes, formatDateTime } from '../lib/format'
import { hostOf } from '../lib/url'
import { useToast } from '../composables/useToast'
import { useTransfersStore } from '../stores/transfers'
import type { TransferFilter } from '../stores/transfers'

const transfers = useTransfersStore()
const router = useRouter()
const toast = useToast()

const TABS: { key: TransferFilter; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'active', label: '进行中' },
  { key: 'completed', label: '已完成' },
  { key: 'failed', label: '失败' },
]

const STATE_LABELS: Record<TransferState, string> = {
  pending: '排队中',
  running: '下载中',
  paused: '已暂停',
  completed: '已完成',
  failed: '失败',
  canceled: '已取消',
}

const createOpen = ref(false)
const deleteOpen = ref(false)
const deleteTarget = ref<TransferItem | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)
const busyIds = ref<string[]>([])

const summary = computed(() => {
  if (!transfers.loaded) return ''
  if (transfers.total === 0) return ''
  const active = transfers.counts.active
  const suffix = active > 0 ? `，${active} 个任务进行中` : ''
  return `共 ${transfers.total} 条${suffix}`
})

onMounted(() => {
  void transfers.load()
  void transfers.loadQBStatus()
  transfers.startPolling()
})

onBeforeUnmount(() => {
  transfers.stopPolling()
})

function isBusy(id: string): boolean {
  return busyIds.value.includes(id)
}

function withBusy(id: string, run: () => Promise<void>): void {
  if (isBusy(id)) return
  busyIds.value = [...busyIds.value, id]
  void run().finally(() => {
    busyIds.value = busyIds.value.filter((value) => value !== id)
  })
}

function onPause(item: TransferItem): void {
  withBusy(item.id, async () => {
    try {
      await transfers.pause(item.id)
      toast.push('已暂停离线下载')
    } catch (cause) {
      toast.push(messageForError(cause, '暂停失败，请稍后重试'), 'error')
    }
  })
}

function onResume(item: TransferItem): void {
  withBusy(item.id, async () => {
    try {
      await transfers.resume(item.id)
      toast.push('已继续离线下载')
    } catch (cause) {
      toast.push(messageForError(cause, '继续失败，请稍后重试'), 'error')
    }
  })
}

function askDelete(item: TransferItem): void {
  deleteTarget.value = item
  deleteError.value = null
  deleteOpen.value = true
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleteBusy.value) return

  deleteBusy.value = true
  deleteError.value = null
  try {
    await transfers.remove(target.id)
    deleteOpen.value = false
    toast.push('已删除离线下载任务')
  } catch (cause) {
    deleteError.value = messageForError(cause, '删除失败，请稍后重试')
  } finally {
    deleteBusy.value = false
  }
}

function openFolder(item: TransferItem): void {
  const parent = item.parent_id > 0 ? item.parent_id : 0
  void router.push(parent > 0 ? `/files/${parent}` : '/files')
}

function onCreated(item: TransferItem): void {
  createOpen.value = false
  const label = isTorrentSource(item)
    ? '已加入种子下载'
    : item.kind === 'qb'
      ? '已加入磁力下载'
      : '已加入离线下载'
  toast.push(`${label}：${item.name || '下载任务'}`)
}

/** qBittorrent tasks point at either a magnet link or an uploaded `.torrent`. */
function isTorrentSource(item: TransferItem): boolean {
  return item.kind === 'qb' && !item.source_url.trim().toLowerCase().startsWith('magnet:')
}

function sourceLabel(item: TransferItem): string {
  if (item.kind === 'qb') return isTorrentSource(item) ? '种子文件' : '磁力链接'
  return hostOf(item.source_url)
}

function sourceTitle(item: TransferItem): string {
  if (item.source_url) return item.source_url
  return item.kind === 'qb' ? '种子文件' : ''
}

function kindTag(item: TransferItem): string {
  if (item.kind !== 'qb') return ''
  return isTorrentSource(item) ? '种子' : '磁力'
}

/** qBittorrent tasks are also cancelled inside qB itself. */
const deleteDetail = computed(() =>
  deleteTarget.value?.kind === 'qb'
    ? '将同时停止并清理 qBittorrent 中的任务；已下载完成的文件会保留在网盘中。'
    : '已下载完成的文件会保留在网盘中。',
)

function canPause(item: TransferItem): boolean {
  return item.state === 'pending' || item.state === 'running'
}

function canResume(item: TransferItem): boolean {
  return item.state === 'paused'
}

function canOpenFolder(item: TransferItem): boolean {
  return item.state === 'completed' && item.node_id !== null
}

function sizeText(item: TransferItem): string {
  const received = formatBytes(item.received_bytes)
  if (item.size <= 0) return `${received} / 未知`
  return `${received} / ${formatBytes(item.size)}`
}

/** null means the server never learned Content-Length (unknown size). */
function progressPercent(item: TransferItem): number | null {
  if (item.state === 'completed') return 100
  if (item.size <= 0) return null
  return Math.max(0, Math.min(100, Math.round((item.received_bytes / item.size) * 100)))
}

function progressText(item: TransferItem): string {
  if (item.state === 'pending') return '等待中'
  if (item.state === 'paused') return '已暂停'
  if (item.state === 'failed') return '下载失败'
  if (item.state === 'canceled') return '已取消'
  const percent = progressPercent(item)
  return percent === null ? '下载中' : `${percent}%`
}

function isIndeterminate(item: TransferItem): boolean {
  return (
    (item.state === 'running' || item.state === 'pending' || item.state === 'paused') &&
    progressPercent(item) === null
  )
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">离线下载</h1>
        <p class="page-sub">
          <template v-if="summary">{{ summary }}</template>
          <template v-else>由服务器从链接下载文件，完成后保存到网盘。</template>
        </p>
      </div>

      <div class="page-actions">
        <button class="btn btn-primary" type="button" @click="createOpen = true">
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <path
              d="M7 2.2v6.2M4.4 6 7 8.6 9.6 6"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
            <path
              d="M2.6 10.4v.6a1.2 1.2 0 0 0 1.2 1.2h6.4a1.2 1.2 0 0 0 1.2-1.2v-.6"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
            />
          </svg>
          新建任务
        </button>
      </div>
    </header>

    <div class="tabs" role="group" aria-label="离线下载筛选">
      <button
        v-for="tab in TABS"
        :key="tab.key"
        class="tab"
        :class="{ 'is-active': transfers.filter === tab.key }"
        type="button"
        :aria-pressed="transfers.filter === tab.key"
        @click="transfers.setFilter(tab.key)"
      >
        {{ tab.label }}<span class="tab-count numeric">{{ transfers.counts[tab.key] }}</span>
      </button>
    </div>

    <section class="panel" aria-label="离线下载列表">
      <div v-if="transfers.loading && !transfers.loaded" class="skeleton" aria-hidden="true">
        <div v-for="row in 4" :key="row" class="skeleton-row">
          <SkeletonBlock :width="row % 3 === 0 ? '42%' : '58%'" height="13px" />
          <SkeletonBlock width="90px" height="13px" />
          <SkeletonBlock width="70px" height="13px" />
          <SkeletonBlock width="110px" height="8px" />
          <SkeletonBlock width="96px" height="26px" />
        </div>
        <p class="visually-hidden">加载中…</p>
      </div>

      <div v-else-if="transfers.error && transfers.items.length === 0" class="state" role="alert">
        <h2 class="state-title">加载失败</h2>
        <p class="state-desc">{{ transfers.error }}</p>
        <button class="btn btn-secondary state-action" type="button" @click="transfers.load()">重试</button>
      </div>

      <EmptyState
        v-else-if="transfers.items.length === 0"
        icon="download"
        title="还没有离线下载任务"
        description="在「我的文件」中点击「离线下载」，粘贴链接即可让服务器帮你下载。"
        action-label="去我的文件"
        @action="router.push('/files')"
      />

      <template v-else>
        <div class="cols list-head" aria-hidden="true">
          <span>名称</span>
          <span class="col-source">来源</span>
          <span class="col-mid col-size">大小</span>
          <span class="col-mid col-state">状态</span>
          <span class="col-progress">进度</span>
          <span class="col-mid col-updated">更新时间</span>
          <span class="col-action">操作</span>
        </div>

        <ul v-if="transfers.visible.length > 0" class="rows">
          <li v-for="item in transfers.visible" :key="item.id" class="cols row">
            <span class="cell-name">
              <svg width="18" height="18" viewBox="0 0 20 20" aria-hidden="true">
                <path
                  d="M10 2.8v7.4M6.8 7.4 10 10.6l3.2-3.2"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
                <path
                  d="M4.4 12.6v1.4a1.4 1.4 0 0 0 1.4 1.4h8.4a1.4 1.4 0 0 0 1.4-1.4v-1.4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                />
              </svg>
              <span class="name" :title="item.name">{{ item.name }}</span>
              <span v-if="kindTag(item)" class="kind-tag">{{ kindTag(item) }}</span>
            </span>

            <span class="cell-meta col-source" :title="sourceTitle(item)">
              {{ sourceLabel(item) }}
            </span>

            <span class="cell-meta col-mid col-size numeric">{{ sizeText(item) }}</span>

            <span class="col-mid col-state">
              <span class="status-badge" :class="`is-${item.state}`">
                <span class="status-dot" aria-hidden="true"></span>
                {{ STATE_LABELS[item.state] }}
              </span>
            </span>

            <span class="col-progress">
              <span class="progress-head">
                <span class="progress-text numeric">{{ progressText(item) }}</span>
              </span>
              <span class="progress-track" aria-hidden="true">
                <span
                  v-if="isIndeterminate(item)"
                  class="progress-fill is-indeterminate"
                ></span>
                <span
                  v-else-if="progressPercent(item) !== null"
                  class="progress-fill"
                  :class="`is-${item.state}`"
                  :style="{ width: `${progressPercent(item)}%` }"
                ></span>
              </span>
            </span>

            <span class="cell-meta col-mid col-updated numeric">
              {{ formatDateTime(item.updated_at) }}
            </span>

            <span class="col-action">
              <button
                v-if="canOpenFolder(item)"
                class="row-button"
                type="button"
                @click="openFolder(item)"
              >
                打开所在目录
              </button>
              <button
                v-if="canPause(item)"
                class="row-button"
                type="button"
                :disabled="isBusy(item.id)"
                :aria-label="`暂停 ${item.name}`"
                @click="onPause(item)"
              >
                暂停
              </button>
              <button
                v-if="canResume(item)"
                class="row-button"
                type="button"
                :disabled="isBusy(item.id)"
                :aria-label="`继续 ${item.name}`"
                @click="onResume(item)"
              >
                继续
              </button>
              <button
                class="row-button is-danger"
                type="button"
                :disabled="isBusy(item.id)"
                :aria-label="`删除 ${item.name}`"
                @click="askDelete(item)"
              >
                删除
              </button>
            </span>

            <span class="mobile-line">{{ sizeText(item) }} · {{ formatDateTime(item.updated_at) }}</span>
          </li>
        </ul>

        <EmptyState
          v-else
          icon="download"
          title="这个分组是空的"
          description="切换到其它分组看看。"
        />

        <div v-if="transfers.hasMore" class="load-more">
          <button class="btn btn-secondary" type="button" :disabled="transfers.loadingMore" @click="transfers.loadMore">
            {{ transfers.loadingMore ? '加载中…' : '加载更多' }}
          </button>
        </div>
      </template>
    </section>

    <OfflineDownloadDialog
      :open="createOpen"
      :parent="0"
      :allow-magnet="transfers.qbEnabled"
      :allow-torrent="transfers.qbEnabled"
      @close="createOpen = false"
      @created="onCreated"
    />

    <ConfirmDialog
      :open="deleteOpen"
      title="删除离线下载任务？"
      :message="deleteTarget ? `「${deleteTarget.name}」的任务记录会被删除。` : ''"
      :detail="deleteDetail"
      confirm-label="删除"
      danger
      :busy="deleteBusy"
      :error="deleteError"
      @close="deleteOpen = false"
      @confirm="confirmDelete"
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
  font-weight: 600;
  letter-spacing: -0.015em;
}

.page-sub {
  margin: 4px 0 0;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.page-actions {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  gap: 8px;
}

.tabs {
  display: flex;
  gap: 6px;
  align-items: center;
}

.tab {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  padding: 6px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  background: var(--color-surface);
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.tab:hover {
  border-color: var(--color-border-strong);
  color: var(--color-text);
}

.tab.is-active {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.tab-count {
  font-size: var(--fs-xs);
}

.panel {
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.cols {
  display: grid;
  grid-template-columns:
    minmax(0, 1.5fr) 110px 140px 82px 150px 138px 156px;
  column-gap: 14px;
  align-items: start;
}

.list-head {
  padding: 10px 16px;
  background: var(--color-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-xs);
  color: var(--color-muted);
  align-items: center;
}

.col-mid {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.col-action {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  justify-content: flex-end;
}

.rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

.row {
  min-height: 56px;
  padding: 9px 16px;
  border-top: 1px solid var(--color-border);
}

.row:first-child {
  border-top: 0;
}

.row:hover {
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

.kind-tag {
  flex: none;
  padding: 1px 6px;
  border-radius: 5px;
  background: var(--color-primary-soft);
  font-size: var(--fs-xs);
  line-height: 1.5;
  color: var(--color-primary-ink);
}

.cell-meta {
  overflow: hidden;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-badge {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  font-size: var(--fs-xs);
  color: var(--color-muted);
  white-space: nowrap;
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.status-badge.is-running {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.status-badge.is-completed {
  border-color: var(--color-success);
  background: var(--color-success-soft);
  color: var(--color-success);
}

.status-badge.is-failed {
  border-color: var(--color-danger);
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.status-badge.is-paused {
  border-color: var(--color-border-strong);
  color: var(--color-text);
}

.col-progress {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}

.progress-head {
  display: flex;
  justify-content: space-between;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.progress-text {
  font-variant-numeric: tabular-nums;
}

.progress-track {
  display: block;
  overflow: hidden;
  height: 5px;
  background: var(--color-border);
  border-radius: var(--radius-pill);
}

.progress-fill {
  display: block;
  height: 100%;
  background: var(--color-primary);
  border-radius: var(--radius-pill);
  transition: width 300ms var(--ease);
}

.progress-fill.is-completed {
  background: var(--color-success);
}

.progress-fill.is-paused {
  background: var(--color-border-strong);
}

.progress-fill.is-indeterminate {
  width: 40%;
  background: linear-gradient(
    90deg,
    transparent 0%,
    var(--color-primary) 50%,
    transparent 100%
  );
  animation: progress-slide 1.4s ease-in-out infinite;
}

@keyframes progress-slide {
  0% {
    transform: translateX(-100%);
  }

  100% {
    transform: translateX(250%);
  }
}

.row-button {
  padding: 4px 9px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  color: var(--color-text);
  font-size: var(--fs-xs);
  white-space: nowrap;
}

.row-button:hover:not(:disabled) {
  border-color: var(--color-border-strong);
}

.row-button:disabled {
  opacity: 0.55;
  cursor: default;
}

.row-button.is-danger {
  color: var(--color-danger);
}

.row-button.is-danger:hover:not(:disabled) {
  border-color: var(--color-danger);
}

.mobile-line {
  display: none;
}

.skeleton {
  display: flex;
  flex-direction: column;
}

.skeleton-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 16px;
  border-top: 1px solid var(--color-border);
}

.skeleton-row:first-child {
  border-top: 0;
}

.state {
  padding: 46px 24px;
  text-align: center;
}

.state-title {
  margin: 0 0 6px;
  font-size: var(--fs-lg);
}

.state-desc {
  margin: 0 0 16px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.state-action {
  display: inline-flex;
}

.load-more {
  display: flex;
  justify-content: center;
  padding: 14px;
  border-top: 1px solid var(--color-border);
}

@media (max-width: 1180px) {
  .cols {
    grid-template-columns: minmax(0, 1.4fr) 96px 130px 78px 130px 128px 150px;
    column-gap: 12px;
  }
}

@media (max-width: 1024px) {
  .cols {
    grid-template-columns: minmax(0, 1.5fr) 130px 78px 130px 128px 150px;
  }

  .col-source {
    display: none;
  }
}

@media (max-width: 860px) {
  .cols {
    grid-template-columns: minmax(0, 1.5fr) 120px 74px 120px 146px;
  }

  .col-updated {
    display: none;
  }
}

@media (max-width: 700px) {
  .page-head {
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
  }

  .page-actions {
    width: 100%;
  }

  .page-actions .btn {
    flex: 1;
  }

  .tabs {
    overflow-x: auto;
    white-space: nowrap;
  }

  .cols {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      'main actions'
      'state progress'
      'meta meta';
    row-gap: 8px;
    padding: 12px 14px;
  }

  .list-head {
    display: none;
  }

  .row {
    align-items: start;
  }

  .cell-name {
    grid-area: main;
  }

  .col-size {
    display: none;
  }

  .col-state {
    grid-area: state;
    justify-self: start;
    text-align: left;
  }

  .col-progress {
    grid-area: progress;
    width: 112px;
  }

  .col-action {
    grid-area: actions;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .col-updated {
    display: none;
  }

  .mobile-line {
    display: block;
    grid-area: meta;
    font-size: var(--fs-xs);
    color: var(--color-muted);
  }
}
</style>
