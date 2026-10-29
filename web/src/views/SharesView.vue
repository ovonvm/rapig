<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import BatchBar from '../components/BatchBar.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import type { BatchAction } from '../components/BatchBar.vue'
import type { ShareInfo, ShareStatusFilter } from '../api/client'
import { copyText } from '../lib/clipboard'
import { runWithConcurrency } from '../lib/concurrency'
import { messageForShareAction } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import {
  SHARE_STATUS_LABELS,
  absoluteShareUrl,
  shareDownloadsLabel,
  shareStatus,
} from '../lib/shareStatus'
import type { ShareStatus } from '../lib/shareStatus'
import { useSharesStore } from '../stores/shares'
import { useToast } from '../composables/useToast'

const BATCH_CONCURRENCY = 3
const STATUS_TABS: { key: ShareStatusFilter; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'active', label: '有效' },
  { key: 'revoked', label: '已撤销' },
  { key: 'expired', label: '已过期' },
]

const router = useRouter()
const shares = useSharesStore()
const toast = useToast()

const copiedId = ref<string | null>(null)
const selectedIds = ref<string[]>([])

const revokeTarget = ref<ShareInfo | null>(null)
const revokeBusy = ref(false)
const revokeError = ref<string | null>(null)

const purgeTargets = ref<ShareInfo[]>([])
const purgeBusy = ref(false)
const purgeError = ref<string | null>(null)

const selectedSet = computed(() => new Set(selectedIds.value))
const selectedShares = computed(() => shares.items.filter((item) => selectedSet.value.has(item.id)))
const allSelected = computed(() => shares.items.length > 0 && selectedIds.value.length === shares.items.length)
const revokableSelected = computed(() => selectedShares.value.filter((item) => shareStatus(item) === 'active'))
const purgeableSelected = computed(() =>
  selectedShares.value.filter((item) => {
    const status = shareStatus(item)
    return status === 'revoked' || status === 'expired'
  }),
)

const batchActions = computed<BatchAction[]>(() => [
  {
    key: 'revoke',
    label: '撤销',
    icon: 'trash',
    disabled: revokableSelected.value.length === 0,
    title: '只撤销仍然有效的分享',
  },
  {
    key: 'purge',
    label: '清理',
    icon: 'purge',
    danger: true,
    disabled: purgeableSelected.value.length === 0,
    title: '只有已撤销或已过期的分享可以清理',
  },
])

const purgeTitle = computed(() =>
  purgeTargets.value.length > 1 ? `清理 ${purgeTargets.value.length} 条分享？` : '清理分享记录？',
)
const purgeMessage = computed(() => {
  if (purgeTargets.value.length !== 1) {
    return '清理后这些分享记录会从列表中永久消失，链接也无法再恢复。'
  }
  return `「${purgeTargets.value[0]?.name ?? '该分享'}」的记录会被永久删除，链接也无法再恢复。`
})

function statusOf(share: ShareInfo): ShareStatus {
  return shareStatus(share)
}

function kindLabel(share: ShareInfo): string {
  if (share.kind === 'multi') return '合并分享'
  return share.kind === 'dir' ? '文件夹' : '文件'
}

function itemCountLabel(share: ShareInfo): string {
  const count = share.item_count ?? 0
  return `共 ${count} 项`
}

function toggleSelect(share: ShareInfo): void {
  selectedIds.value = selectedSet.value.has(share.id)
    ? selectedIds.value.filter((id) => id !== share.id)
    : [...selectedIds.value, share.id]
}

function toggleAll(): void {
  selectedIds.value = allSelected.value ? [] : shares.items.map((item) => item.id)
}

function clearSelection(): void {
  selectedIds.value = []
}

async function switchStatus(key: ShareStatusFilter): Promise<void> {
  if (shares.status === key) return
  clearSelection()
  await shares.setStatus(key)
}

async function refresh(): Promise<void> {
  clearSelection()
  await shares.load()
}

async function copyLink(share: ShareInfo): Promise<void> {
  const ok = await copyText(absoluteShareUrl(share))
  if (!ok) {
    toast.push('复制失败，请手动选择链接复制', 'error')
    return
  }
  copiedId.value = share.id
  window.setTimeout(() => {
    if (copiedId.value === share.id) copiedId.value = null
  }, 1800)
  toast.push('已复制分享链接')
}

function openRevoke(share: ShareInfo): void {
  revokeTarget.value = share
  revokeError.value = null
  revokeBusy.value = false
}

async function confirmRevoke(): Promise<void> {
  const target = revokeTarget.value
  if (!target || revokeBusy.value) return

  revokeBusy.value = true
  revokeError.value = null
  try {
    await shares.revoke(target.id)
    revokeTarget.value = null
    toast.push(`已撤销「${target.name}」的分享链接`)
  } catch (cause) {
    revokeError.value = messageForShareAction(cause, '撤销失败，请稍后重试')
  } finally {
    revokeBusy.value = false
  }
}

async function restore(share: ShareInfo): Promise<void> {
  try {
    await shares.restore(share.id)
    toast.push(`已恢复「${share.name}」的分享链接`)
  } catch (cause) {
    toast.push(messageForShareAction(cause, '恢复失败，请稍后重试'), 'error')
  }
}

function openPurge(sharesToPurge: ShareInfo[]): void {
  if (sharesToPurge.length === 0) return
  purgeTargets.value = sharesToPurge
  purgeError.value = null
  purgeBusy.value = false
}

async function confirmPurge(): Promise<void> {
  if (purgeTargets.value.length === 0 || purgeBusy.value) return

  const targets = purgeTargets.value
  purgeBusy.value = true
  purgeError.value = null

  try {
    if (targets.length === 1) {
      const target = targets[0]!
      await shares.purge(target.id)
      toast.push(`已清理「${target.name}」的分享记录`)
    } else {
      const result = await runWithConcurrency(targets, BATCH_CONCURRENCY, (item) => shares.purge(item.id))
      if (result.failures.length === 0) {
        toast.push(`已清理 ${result.succeeded} 条分享记录`)
      } else {
        const first = result.failures[0]!.error
        toast.push(
          `已清理 ${result.succeeded} 条，${result.failures.length} 条失败：${messageForShareAction(first)}`,
          'error',
        )
      }
    }
    purgeTargets.value = []
    clearSelection()
  } catch (cause) {
    purgeError.value = messageForShareAction(cause, '清理失败，请稍后重试')
  } finally {
    purgeBusy.value = false
  }
}

async function onBatchAction(key: string): Promise<void> {
  if (key === 'revoke') {
    const targets = revokableSelected.value
    if (targets.length === 0) return
    const skipped = selectedShares.value.length - targets.length
    const result = await runWithConcurrency(targets, BATCH_CONCURRENCY, (item) => shares.revoke(item.id))
    clearSelection()
    if (result.failures.length === 0) {
      const suffix = skipped > 0 ? `，已跳过 ${skipped} 条非有效分享` : ''
      toast.push(`已撤销 ${result.succeeded} 条分享链接${suffix}`)
    } else {
      toast.push(
        `已撤销 ${result.succeeded} 条，${result.failures.length} 条失败：${messageForShareAction(result.failures[0]!.error)}`,
        'error',
      )
    }
    return
  }

  if (key === 'purge') {
    const targets = purgeableSelected.value
    if (targets.length === 0) return
    openPurge(targets)
  }
}

watch(
  () => shares.items,
  (items) => {
    const alive = new Set(items.map((item) => item.id))
    selectedIds.value = selectedIds.value.filter((id) => alive.has(id))
  },
)

onMounted(() => {
  void shares.load()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">我的分享</h1>
        <p v-if="!shares.loading && !shares.error" class="page-sub">
          {{ shares.total > 0 ? `共 ${shares.total} 条分享` : '把文件或文件夹分享给其他人' }}
        </p>
      </div>
      <div class="page-actions">
        <div class="filter-tabs" role="tablist" aria-label="按状态筛选分享">
          <button
            v-for="tab in STATUS_TABS"
            :key="tab.key"
            type="button"
            role="tab"
            class="filter-tab"
            :class="{ 'is-active': shares.status === tab.key }"
            :aria-selected="shares.status === tab.key"
            :disabled="shares.loading"
            @click="switchStatus(tab.key)"
          >
            {{ tab.label }}
          </button>
        </div>
        <button type="button" class="btn btn-secondary refresh-button" :disabled="shares.loading" @click="refresh">
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <path
              d="M11.8 7a4.8 4.8 0 1 1-1.4-3.4M11.9 1.9v3h-3"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          刷新
        </button>
      </div>
    </header>

    <section class="panel" aria-label="分享列表">
      <div v-if="shares.loading && !shares.loaded" class="skeleton-list" aria-hidden="true">
        <div v-for="index in 4" :key="index" class="skeleton-row">
          <span class="skeleton-block skeleton-icon"></span>
          <span class="skeleton-block skeleton-cell skeleton-name"></span>
          <span class="skeleton-block skeleton-cell"></span>
          <span class="skeleton-block skeleton-cell skeleton-short"></span>
        </div>
      </div>

      <div v-else-if="shares.error && shares.items.length === 0" class="state" role="alert">
        <p class="state-title">加载失败</p>
        <p class="state-desc">{{ shares.error }}</p>
        <button type="button" class="btn btn-secondary state-action" @click="refresh">重试</button>
      </div>

      <template v-else-if="shares.items.length === 0">
        <EmptyState
          icon="share"
          title="还没有分享"
          description="在「我的文件」里选择文件或文件夹，点击分享即可创建链接。"
          action-label="去我的文件"
          @action="router.push('/files')"
        />
      </template>

      <template v-else>
        <div class="table-scroll">
          <table class="share-table">
            <thead>
              <tr class="cols">
                <th class="col-check" scope="col">
                  <input
                    class="row-check"
                    type="checkbox"
                    aria-label="全选分享"
                    :checked="allSelected"
                    @change="toggleAll"
                  />
                </th>
                <th class="col-name" scope="col">名称</th>
                <th class="col-kind" scope="col">类型</th>
                <th class="col-status" scope="col">状态</th>
                <th class="col-count" scope="col">下载次数</th>
                <th class="col-count" scope="col">访问次数</th>
                <th class="col-time" scope="col">创建时间</th>
                <th class="col-actions" scope="col">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="share in shares.items"
                :key="share.id"
                class="cols row"
                :class="{ 'is-selected': selectedSet.has(share.id) }"
              >
                <td class="col-check">
                  <input
                    class="row-check"
                    type="checkbox"
                    :aria-label="`选择 ${share.name}`"
                    :checked="selectedSet.has(share.id)"
                    @change="toggleSelect(share)"
                  />
                </td>
                <td class="col-name">
                  <span class="name-cell">
                    <svg
                      class="kind-icon"
                      :class="{ 'is-dir': share.kind === 'dir', 'is-multi': share.kind === 'multi' }"
                      width="18"
                      height="18"
                      viewBox="0 0 18 18"
                      aria-hidden="true"
                    >
                      <template v-if="share.kind === 'multi'">
                        <rect
                          x="2.6"
                          y="4.2"
                          width="9.6"
                          height="8"
                          rx="1.6"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="1.3"
                        />
                        <path
                          d="M5.6 4.2V3.6A1.4 1.4 0 0 1 7 2.2h7.4A1.4 1.4 0 0 1 15.8 3.6v7a1.4 1.4 0 0 1-1.4 1.4h-.6"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="1.3"
                          stroke-linecap="round"
                        />
                      </template>
                      <path
                        v-else-if="share.kind === 'dir'"
                        d="M2 5.2a1.6 1.6 0 0 1 1.6-1.6h2.7l1.5 1.7h5.6a1.6 1.6 0 0 1 1.6 1.6v4.5a1.6 1.6 0 0 1-1.6 1.6H3.6A1.6 1.6 0 0 1 2 11.4V5.2Z"
                        fill="currentColor"
                        fill-opacity="0.16"
                        stroke="currentColor"
                        stroke-width="1.3"
                        stroke-linejoin="round"
                      />
                      <path
                        v-else
                        d="M4.4 2.6h5.4l3.8 3.8v8.2a1.4 1.4 0 0 1-1.4 1.4H4.4a1.4 1.4 0 0 1-1.4-1.4V4a1.4 1.4 0 0 1 1.4-1.4Z"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="1.3"
                        stroke-linejoin="round"
                      />
                    </svg>
                    <span class="share-name">
                      {{ share.name }}
                      <span v-if="share.kind === 'multi'" class="multi-count">{{ itemCountLabel(share) }}</span>
                    </span>
                  </span>
                </td>
                <td class="col-kind">{{ kindLabel(share) }}</td>
                <td class="col-status">
                  <span class="status" :class="`status-${statusOf(share)}`">
                    {{ SHARE_STATUS_LABELS[statusOf(share)] }}
                  </span>
                </td>
                <td class="col-count">{{ shareDownloadsLabel(share) }}</td>
                <td class="col-count">{{ share.visits_count }}</td>
                <td class="col-time">{{ formatDateTime(share.created_at) }}</td>
                <td class="col-actions">
                  <div class="row-actions">
                    <button
                      type="button"
                      class="icon-button"
                      aria-label="复制分享链接"
                      title="复制分享链接"
                      @click="copyLink(share)"
                    >
                      <svg v-if="copiedId === share.id" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
                        <path
                          d="m2.6 7.4 2.8 2.8 5.9-6"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="1.5"
                          stroke-linecap="round"
                          stroke-linejoin="round"
                        />
                      </svg>
                      <svg v-else width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
                        <rect
                          x="5.2"
                          y="5.2"
                          width="7"
                          height="7"
                          rx="1.4"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="1.2"
                        />
                        <path
                          d="M9.2 5.2V3.2a1.4 1.4 0 0 0-1.4-1.4H3.2a1.4 1.4 0 0 0-1.4 1.4v4.6a1.4 1.4 0 0 0 1.4 1.4h2"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="1.2"
                        />
                      </svg>
                    </button>
                    <button
                      v-if="statusOf(share) === 'active' || statusOf(share) === 'exhausted'"
                      type="button"
                      class="text-button is-danger"
                      @click="openRevoke(share)"
                    >
                      撤销
                    </button>
                    <button
                      v-if="statusOf(share) === 'revoked'"
                      type="button"
                      class="text-button"
                      @click="restore(share)"
                    >
                      恢复
                    </button>
                    <button
                      v-if="statusOf(share) !== 'active' && statusOf(share) !== 'exhausted'"
                      type="button"
                      class="text-button is-danger"
                      @click="openPurge([share])"
                    >
                      清理
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="shares.items.length < shares.total" class="load-more">
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="shares.loadingMore"
            @click="shares.loadMore()"
          >
            {{ shares.loadingMore ? '加载中…' : '加载更多' }}
          </button>
        </div>
      </template>
    </section>

    <BatchBar
      :count="selectedIds.length"
      :total="shares.items.length"
      :actions="batchActions"
      :all-selected="allSelected"
      @action="onBatchAction"
      @clear="clearSelection"
      @select-all="toggleAll"
    />

    <ConfirmDialog
      :open="revokeTarget !== null"
      title="撤销分享？"
      :message="`「${revokeTarget?.name ?? '该分享'}」的分享链接会立即失效。`"
      detail="已打开的访客会看到「分享不存在或已失效」；记录仍会保留，之后可以恢复。"
      confirm-label="撤销分享"
      danger
      :busy="revokeBusy"
      :error="revokeError"
      @close="revokeTarget = null"
      @confirm="confirmRevoke"
    />

    <ConfirmDialog
      :open="purgeTargets.length > 0"
      :title="purgeTitle"
      :message="purgeMessage"
      detail="这个操作无法撤销。"
      confirm-label="清理"
      danger
      :busy="purgeBusy"
      :error="purgeError"
      @close="purgeTargets = []"
      @confirm="confirmPurge"
    />
  </div>
</template>

<style scoped>
.page {
  max-width: var(--page-max);
  margin: 0 auto;
}

.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.page-heading {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.page-title {
  margin: 0;
  font-size: var(--fs-xl);
  font-weight: 600;
  letter-spacing: -0.015em;
}

.page-sub {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.page-actions {
  display: inline-flex;
  align-items: center;
  gap: 10px;
}

.filter-tabs {
  display: inline-flex;
  padding: 2px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg);
}

.filter-tab {
  height: 28px;
  padding: 0 10px;
  border-radius: 5px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  transition: color var(--dur) var(--ease), background var(--dur) var(--ease);
}

.filter-tab:hover:not(:disabled) {
  color: var(--color-text);
}

.filter-tab.is-active {
  background: var(--color-surface);
  color: var(--color-text);
  font-weight: 500;
}

.filter-tab:disabled {
  opacity: 0.6;
}

.refresh-button {
  flex: none;
  gap: 6px;
}

.panel {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
}

.table-scroll {
  overflow-x: auto;
}

.share-table {
  width: 100%;
  min-width: 880px;
  border-collapse: collapse;
  font-size: var(--fs-sm);
}

.cols {
  display: grid;
  grid-template-columns: 26px minmax(0, 1.6fr) 76px 76px 104px 76px 148px 152px;
  align-items: center;
  column-gap: 14px;
  padding: 0 16px;
}

.share-table thead .cols {
  height: 38px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg);
  color: var(--color-muted);
  font-weight: 500;
  font-size: var(--fs-xs);
  text-align: left;
}

.share-table thead th {
  text-align: left;
  font-weight: 500;
}

.row {
  height: 52px;
  border-bottom: 1px solid var(--color-border);
  transition: background var(--dur) var(--ease);
}

.row:last-child {
  border-bottom: 0;
}

.row:hover {
  background: var(--color-primary-soft);
}

.row.is-selected {
  background: var(--color-primary-soft);
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

.col-name {
  min-width: 0;
}

.name-cell {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  min-width: 0;
  max-width: 100%;
}

.kind-icon {
  flex: none;
  color: var(--color-muted);
}

.kind-icon.is-dir {
  color: var(--color-primary);
}

.kind-icon.is-multi {
  color: var(--color-primary-ink);
}

.share-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.multi-count {
  margin-left: 6px;
  padding: 1px 6px;
  border-radius: var(--radius-badge);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-size: var(--fs-xs);
  white-space: nowrap;
}

.col-kind {
  color: var(--color-muted);
}

.status {
  display: inline-flex;
  align-items: center;
  height: 21px;
  padding: 0 8px;
  border-radius: var(--radius-pill);
  font-size: var(--fs-xs);
  white-space: nowrap;
}

.status-active {
  background: var(--color-success-soft);
  color: var(--color-success);
}

.status-expired {
  background: var(--color-bg);
  color: var(--color-muted);
}

.status-revoked {
  background: var(--color-bg);
  color: var(--color-muted);
}

.status-exhausted {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.col-count {
  font-variant-numeric: tabular-nums;
  color: var(--color-muted);
}

.col-time {
  white-space: nowrap;
  color: var(--color-muted);
  font-variant-numeric: tabular-nums;
}

.row-actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.icon-button {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
}

.icon-button:hover {
  background: var(--color-bg);
  color: var(--color-primary-ink);
}

.text-button {
  padding: 0 6px;
  height: 28px;
  border-radius: var(--radius-sm);
  color: var(--color-primary-ink);
  font-size: var(--fs-xs);
}

.text-button:hover:not(:disabled) {
  background: var(--color-bg);
}

.text-button.is-danger {
  color: var(--color-danger);
}

.text-button:disabled {
  color: var(--color-muted);
  cursor: default;
  opacity: 0.6;
}

.load-more {
  display: flex;
  justify-content: center;
  padding: 14px;
  border-top: 1px solid var(--color-border);
}

.skeleton-list {
  padding: 8px 16px;
}

.skeleton-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 0;
  border-bottom: 1px solid var(--color-border);
}

.skeleton-row:last-child {
  border-bottom: 0;
}

.skeleton-icon {
  width: 18px;
  height: 18px;
  border-radius: 5px;
}

.skeleton-cell {
  width: 76px;
  height: 12px;
}

.skeleton-name {
  width: 220px;
}

.skeleton-short {
  width: 96px;
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 54px 20px;
  text-align: center;
}

.state-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
}

.state-desc {
  margin: 0;
  max-width: 420px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.state-action {
  margin-top: 6px;
}

@media (max-width: 760px) {
  .page-head {
    flex-wrap: wrap;
    align-items: flex-start;
  }

  .page-actions {
    width: 100%;
    justify-content: space-between;
  }
}
</style>
