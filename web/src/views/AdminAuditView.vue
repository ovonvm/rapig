<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useToast } from '../composables/useToast'
import { formatDateTime } from '../lib/format'
import {
  adminErrorMessage,
  auditActionLabel,
  auditResultLabel,
  localInputToUnix,
} from '../lib/admin'
import { useAdminStore } from '../stores/admin'
import type { AuditFilters } from '../stores/admin'

const admin = useAdminStore()
const toast = useToast()

const form = reactive({
  actor: '',
  action: '',
  from: '',
  to: '',
})

const loadingMore = ref(false)
const rangeError = ref<string | null>(null)

const empty = computed(() => !admin.auditLoading && !admin.auditError && admin.auditEntries.length === 0)

function buildFilters(): AuditFilters | null {
  rangeError.value = null
  const from = localInputToUnix(form.from)
  const to = localInputToUnix(form.to)
  if (form.from && from === null) {
    rangeError.value = '请填写有效的开始时间'
    return null
  }
  if (form.to && to === null) {
    rangeError.value = '请填写有效的结束时间'
    return null
  }
  if (from !== null && to !== null && from > to) {
    rangeError.value = '开始时间不能晚于结束时间'
    return null
  }
  return { actor: form.actor.trim(), action: form.action, from, to }
}

async function apply(): Promise<void> {
  const filters = buildFilters()
  if (!filters) return
  try {
    await admin.loadAudit(filters)
  } catch (cause) {
    toast.push(adminErrorMessage(cause, '审计日志加载失败，请稍后重试'), 'error')
  }
}

async function reset(): Promise<void> {
  form.actor = ''
  form.action = ''
  form.from = ''
  form.to = ''
  rangeError.value = null
  try {
    await admin.loadAudit({ actor: '', action: '', from: null, to: null })
  } catch (cause) {
    toast.push(adminErrorMessage(cause, '审计日志加载失败，请稍后重试'), 'error')
  }
}

async function loadMore(): Promise<void> {
  loadingMore.value = true
  try {
    await admin.loadMoreAudit()
  } catch (cause) {
    toast.push(adminErrorMessage(cause, '加载更多失败，请稍后重试'), 'error')
  } finally {
    loadingMore.value = false
  }
}

onMounted(async () => {
  await Promise.all([admin.loadAudit({ actor: '', action: '', from: null, to: null }), admin.loadAuditActions()])
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">审计日志</h1>
        <p class="page-sub">共 {{ admin.auditTotal }} 条记录，保留天数由系统设置决定</p>
      </div>
    </header>

    <form class="filters" role="search" aria-label="审计筛选" @submit.prevent="apply">
      <label class="field filter-actor">
        <span class="field-label">操作者</span>
        <input v-model="form.actor" class="input" type="text" placeholder="用户名" autocomplete="off" />
      </label>
      <label class="field">
        <span class="field-label">动作</span>
        <select v-model="form.action" class="input">
          <option value="">全部动作</option>
          <option v-for="action in admin.auditActions" :key="action" :value="action">{{ auditActionLabel(action) }}</option>
        </select>
      </label>
      <label class="field">
        <span class="field-label">开始时间</span>
        <input v-model="form.from" class="input" type="datetime-local" />
      </label>
      <label class="field">
        <span class="field-label">结束时间</span>
        <input v-model="form.to" class="input" type="datetime-local" />
      </label>
      <div class="filter-actions">
        <button type="submit" class="btn btn-primary">查询</button>
        <button type="button" class="btn btn-secondary" @click="reset">重置</button>
      </div>
    </form>
    <p v-if="rangeError" class="field-error" role="alert">{{ rangeError }}</p>

    <section class="panel" aria-label="审计日志列表">
      <div class="table-scroll">
        <table class="table">
          <thead>
            <tr>
              <th scope="col">时间</th>
              <th scope="col">操作者</th>
              <th scope="col">动作</th>
              <th scope="col">对象</th>
              <th scope="col">结果</th>
              <th scope="col">IP</th>
              <th scope="col">request_id</th>
            </tr>
          </thead>

          <tbody v-if="admin.auditLoading && admin.auditEntries.length === 0">
            <tr v-for="row in 4" :key="row" class="skeleton-row">
              <td v-for="col in 7" :key="col"><span class="skeleton-block"></span></td>
            </tr>
          </tbody>

          <tbody v-else-if="admin.auditError">
            <tr>
              <td colspan="7">
                <div class="state" role="alert">
                  <p class="state-title">加载失败</p>
                  <p class="state-copy">{{ admin.auditError }}</p>
                  <button type="button" class="btn btn-secondary" @click="apply">重试</button>
                </div>
              </td>
            </tr>
          </tbody>

          <tbody v-else-if="empty">
            <tr>
              <td colspan="7">
                <div class="state">
                  <p class="state-title">没有符合条件的记录</p>
                  <p class="state-copy">调整操作者、动作或时间范围后再试。</p>
                </div>
              </td>
            </tr>
          </tbody>

          <tbody v-else>
            <tr v-for="(entry, index) in admin.auditEntries" :key="`${entry.at}-${entry.request_id}-${index}`">
              <td class="cell-nowrap numeric">{{ formatDateTime(entry.at) }}</td>
              <td class="cell-name">{{ entry.actor_name || `#${entry.actor_id}` }}</td>
              <td>
                <span class="action-badge" :title="entry.action">{{ auditActionLabel(entry.action) }}</span>
              </td>
              <td class="cell-muted">
                <span class="cell-nowrap">{{ entry.target_type }}<template v-if="entry.target_id"> #{{ entry.target_id }}</template></span>
                <details v-if="entry.detail" class="detail">
                  <summary>详情</summary>
                  <p class="detail-text">{{ entry.detail }}</p>
                </details>
              </td>
              <td>
                <span class="result" :class="`is-${entry.result}`">{{ auditResultLabel(entry.result) }}</span>
              </td>
              <td class="cell-muted numeric cell-nowrap">{{ entry.ip || '—' }}</td>
              <td class="cell-muted">
                <code class="rid" :title="entry.request_id">{{ entry.request_id || '—' }}</code>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="admin.hasMoreAudit" class="load-more">
        <button type="button" class="btn btn-secondary" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? '加载中…' : `加载更多（还有 ${admin.auditTotal - admin.auditEntries.length} 条）` }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-width: var(--page-max);
  margin: 0 auto;
}

.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
}

.page-heading {
  display: flex;
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

.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
}

.filters .field {
  width: 168px;
}

.filter-actor {
  width: 180px;
}

.filter-actions {
  display: flex;
  gap: 8px;
  margin-left: auto;
}

.panel {
  position: relative;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
  padding: 14px 14px 0;
}

.table-scroll {
  overflow-x: auto;
}

.table {
  width: 100%;
  min-width: 900px;
  border-collapse: collapse;
  text-align: left;
}

.table th {
  padding: 8px 10px 10px;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-muted);
  font-size: var(--fs-xs);
  font-weight: 500;
  white-space: nowrap;
}

.table td {
  padding: 10px;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-sm);
  vertical-align: top;
}

.table tbody tr:last-child td {
  border-bottom: 0;
}

.cell-nowrap {
  white-space: nowrap;
}

.cell-name {
  font-weight: 500;
  white-space: nowrap;
}

.cell-muted {
  color: var(--color-muted);
}

.action-badge {
  display: inline-block;
  padding: 1px 7px;
  border-radius: var(--radius-badge);
  background: var(--color-bg);
  color: var(--color-text);
  font-size: var(--fs-xs);
  white-space: nowrap;
}

.result {
  display: inline-flex;
  padding: 1px 7px;
  border-radius: var(--radius-badge);
  background: var(--color-bg);
  color: var(--color-muted);
  font-size: var(--fs-xs);
  white-space: nowrap;
}

.result.is-ok,
.result.is-success {
  background: var(--color-success-soft);
  color: var(--color-success);
}

.result.is-denied,
.result.is-error,
.result.is-failure {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.rid {
  display: inline-block;
  max-width: 168px;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: var(--fs-xs);
  white-space: nowrap;
  vertical-align: bottom;
}

.detail {
  margin-top: 4px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.detail summary {
  cursor: pointer;
}

.detail-text {
  margin: 4px 0 0;
  max-width: 280px;
  white-space: pre-wrap;
  word-break: break-word;
}

.load-more {
  display: flex;
  justify-content: center;
  padding: 12px 0 14px;
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 34px 16px;
  text-align: center;
}

.state-title {
  margin: 0;
  font-size: var(--fs-base);
  font-weight: 600;
}

.state-copy {
  margin: 0 0 4px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.skeleton-block {
  display: block;
  width: 72%;
  height: 10px;
  border-radius: 4px;
  background: var(--color-border);
  animation: skeleton-pulse 1.2s ease-in-out infinite;
}

@keyframes skeleton-pulse {
  0%,
  100% {
    opacity: 0.55;
  }
  50% {
    opacity: 1;
  }
}

@media (max-width: 760px) {
  .filters .field,
  .filter-actor {
    width: calc(50% - 5px);
  }

  .filter-actions {
    width: 100%;
    margin-left: 0;
  }

  .filter-actions .btn {
    flex: 1;
  }
}
</style>
