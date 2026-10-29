<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import BrandMark from '../components/BrandMark.vue'
import { publicDownloadUrl } from '../api/client'
import type { PublicNode, PublicShareMeta } from '../api/client'
import { formatBytes } from '../lib/format'
import { shareExpiryLabel } from '../lib/shareStatus'
import { PACK_DOWNLOAD_LIMIT } from '../lib/download'
import { parseEpoch } from '../lib/time'
import { usePublicShareStore } from '../stores/publicShare'
import { useDownloadsStore } from '../stores/downloads'
import { useToast } from '../composables/useToast'

interface Crumb {
  id: number | null
  name: string
}

const route = useRoute()
const share = usePublicShareStore()
const downloads = useDownloadsStore()
const toast = useToast()

const password = ref('')
const showPassword = ref(false)
const passwordInput = ref<HTMLInputElement | null>(null)
const crumbs = ref<Crumb[]>([])
const now = ref(Date.now())
let ticker: number | null = null

const token = computed(() => {
  const raw = route.params.token
  return Array.isArray(raw) ? (raw[0] ?? '') : (raw ?? '')
})

const isLoading = computed(() => share.loading)
const hasFatalError = computed(() => !share.loading && share.meta === null && Boolean(share.errorCode))
const isMulti = computed(() => !share.locked && share.kind === 'multi' && Boolean(share.meta))
const showDir = computed(
  () => !share.locked && (share.kind === 'dir' || share.kind === 'multi') && Boolean(share.meta),
)
const contentTitle = computed(() => {
  if (isMulti.value) return `共 ${share.meta?.item_count ?? share.entries.length} 项内容`
  return share.meta?.name ?? ''
})
const metaLine = computed(() => {
  if (isMulti.value) return `合并分享 · 剩余次数 ${downloadsLeftLabel.value}`
  return `${expiryLabel.value} · 剩余次数 ${downloadsLeftLabel.value}`
})

const errorTitle = computed(() =>
  share.errorCode === 'share_exhausted' ? '分享下载次数已用完' : '分享不存在或已失效',
)

const errorDescription = computed(() => {
  if (share.errorCode === 'share_exhausted') return '这个分享链接的下载次数已经用完，请联系分享者重新创建。'
  if (share.errorCode === 'too_many_attempts') return share.errorMessage ?? '尝试过于频繁，请稍后再试。'
  return '链接可能已被撤销、已过期，或者分享的内容已被删除。'
})

const downloadsLeftLabel = computed(() => {
  const left = share.meta?.downloads_left
  return left === null || left === undefined ? '不限' : `${left} 次`
})

const expiryLabel = computed(() => {
  const end = parseEpoch(share.meta?.expires_at ?? null)
  if (end === null) return shareExpiryLabel(null)

  const left = end - now.value
  if (left <= 0) return '已过期'
  const hours = Math.floor(left / 3_600_000)
  if (hours < 1) return '剩余不足 1 小时'
  if (hours < 24) return `剩余 ${hours} 小时`
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours > 0 ? `剩余 ${days} 天 ${restHours} 小时` : `剩余 ${days} 天`
})

const expired = computed(() => {
  const end = parseEpoch(share.meta?.expires_at ?? null)
  return end !== null && end <= now.value
})

const exhausted = computed(() => share.meta?.downloads_left === 0)
const canDownload = computed(() => !expired.value && !exhausted.value)
const downloadBlockLabel = computed(() => {
  if (exhausted.value) return '下载次数已用完'
  if (expired.value) return '分享已过期'
  return '下载文件'
})

const currentCrumb = computed(() => crumbs.value[crumbs.value.length - 1] ?? null)

const fileMeta = computed(() => {
  const meta = share.meta
  if (!meta || typeof meta.size !== 'number') return ''
  return formatBytes(meta.size)
})

const canLoadMore = computed(() => share.entries.length < share.entriesTotal)

const fileUrl = computed(() => (share.meta ? publicDownloadUrl(share.token, share.meta.node_id) : ''))

function downloadUrlFor(node: PublicNode): string {
  return publicDownloadUrl(share.token, node.id)
}

/** Folder shares download one file per request; keep the batch small. */
function downloadAll(): void {
  if (!canDownload.value) return
  if (share.entriesTotal > share.entries.length) {
    toast.push('文件较多，请先「加载更多」再打包下载', 'error')
    return
  }
  const files = share.entries.filter((node) => node.kind === 'file')
  if (files.length === 0) {
    toast.push('当前文件夹里没有可下载的文件', 'error')
    return
  }
  if (files.length > PACK_DOWNLOAD_LIMIT) {
    toast.push(`一次最多打包下载 ${PACK_DOWNLOAD_LIMIT} 个文件`, 'error')
    return
  }

  const added = downloads.enqueue(
    files.map((node) => ({ url: downloadUrlFor(node), name: node.name, size: node.size })),
  )
  if (added === 0) return
  toast.push(`已加入下载队列 ${added} 个文件`, 'success', 6000, {
    label: '查看任务',
    run: () => downloads.openPanel(),
  })
}

/** Reset the in-share breadcrumb to the share root. */
function resetCrumbs(): void {
  const meta: PublicShareMeta | null = share.meta
  crumbs.value =
    meta && (meta.kind === 'dir' || meta.kind === 'multi')
      ? [{ id: null, name: meta.kind === 'multi' ? '分享内容' : (meta.name ?? '分享的文件夹') }]
      : []
}

function enterDir(node: PublicNode): void {
  if (node.kind !== 'dir') return
  crumbs.value = [...crumbs.value, { id: node.id, name: node.name }]
  void share.loadNodes(node.id)
}

function goCrumb(index: number): void {
  const target = crumbs.value[index]
  if (!target) return
  crumbs.value = crumbs.value.slice(0, index + 1)
  void share.loadNodes(target.id)
}

async function submitPassword(): Promise<void> {
  if (!password.value || share.unlocking) return
  const ok = await share.unlock(password.value)
  if (ok) {
    password.value = ''
    resetCrumbs()
    return
  }
  passwordInput.value?.focus()
  passwordInput.value?.select()
}

async function load(): Promise<void> {
  if (!token.value) return
  await share.open(token.value)
  resetCrumbs()
  if (share.locked) {
    password.value = ''
    window.setTimeout(() => passwordInput.value?.focus(), 60)
  }
}

watch(token, () => {
  void load()
})

watch(
  () => share.locked,
  (locked) => {
    if (locked) window.setTimeout(() => passwordInput.value?.focus(), 60)
  },
)

onMounted(() => {
  ticker = window.setInterval(() => {
    now.value = Date.now()
  }, 30_000)
  void load()
})

onBeforeUnmount(() => {
  if (ticker !== null) window.clearInterval(ticker)
  ticker = null
  share.reset()
})
</script>

<template>
  <div class="public-page">
    <div class="public-shell" :class="{ 'is-wide': showDir }">
      <header class="brand">
        <BrandMark :size="26" />
        <span class="wordmark">Rapig</span>
        <span class="brand-note">分享</span>
      </header>

      <main class="public-card">
        <div v-if="isLoading" class="loading-card" role="status">
          <span class="loading-dots" aria-hidden="true"><span></span><span></span><span></span></span>
          <p class="loading-text">正在加载分享…</p>
        </div>

        <div v-else-if="hasFatalError" class="error-card" role="alert">
          <span class="state-icon is-danger" aria-hidden="true">
            <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <path d="M12 8v5" stroke-linecap="round" />
              <circle cx="12" cy="16.4" r="0.9" fill="currentColor" stroke="none" />
              <path d="M10.6 3.9 2.9 17.2A1.6 1.6 0 0 0 4.3 19.6h15.4a1.6 1.6 0 0 0 1.4-2.4L13.4 3.9a1.6 1.6 0 0 0-2.8 0Z" stroke-linejoin="round" />
            </svg>
          </span>
          <h1 class="state-title">{{ errorTitle }}</h1>
          <p class="state-desc">{{ errorDescription }}</p>
          <div class="error-actions">
            <button class="btn btn-secondary" type="button" @click="load">重试</button>
            <RouterLink class="btn btn-secondary" to="/">返回首页</RouterLink>
          </div>
        </div>

        <div v-else-if="share.locked" class="locked-card">
          <span class="state-icon is-lock" aria-hidden="true">
            <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <rect x="4.6" y="10.4" width="14.8" height="9.6" rx="2" />
              <path d="M8.4 10.2V7.6a3.6 3.6 0 0 1 7.2 0v2.6" stroke-linecap="round" />
            </svg>
          </span>
          <h1 class="state-title">受密码保护的分享</h1>
          <p class="state-desc">请输入分享者提供的密码，验证后即可查看内容。</p>

          <form class="password-form" novalidate @submit.prevent="submitPassword">
            <label class="visually-hidden" for="public-share-password">分享密码</label>
            <div class="password-row">
              <input
                id="public-share-password"
                ref="passwordInput"
                v-model="password"
                class="input"
                :type="showPassword ? 'text' : 'password'"
                autocomplete="current-password"
                :disabled="share.unlocking"
                placeholder="输入分享密码"
                :aria-invalid="Boolean(share.unlockError)"
                :aria-describedby="share.unlockError ? 'public-share-password-error' : undefined"
              />
              <button
                type="button"
                class="eye-button"
                :aria-label="showPassword ? '隐藏密码' : '显示密码'"
                :aria-pressed="showPassword"
                @click="showPassword = !showPassword"
              >
                <svg v-if="showPassword" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                  <path d="M3.5 3.5l17 17" />
                  <path d="M10.6 6.3A9 9 0 0121 12a15 15 0 01-3.2 3.9M6.1 7.9A14.9 14.9 0 003 12a9 9 0 0010.4 5.7" />
                </svg>
                <svg v-else width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                  <path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z" />
                  <circle cx="12" cy="12" r="2.8" />
                </svg>
              </button>
            </div>

            <p v-if="share.unlockError" id="public-share-password-error" class="field-error" role="alert">
              {{ share.unlockError }}
            </p>

            <button class="btn btn-primary btn-block" type="submit" :disabled="share.unlocking || password.length === 0">
              {{ share.unlocking ? '验证中…' : '查看分享' }}
            </button>
          </form>
        </div>

        <template v-else-if="share.meta">
          <div v-if="share.kind === 'file'" class="file-card">
            <span class="file-icon" aria-hidden="true">
              <svg width="34" height="34" viewBox="0 0 20 20">
                <path
                  d="M5.6 3.2h5.2l4 4v9.2a1.4 1.4 0 0 1-1.4 1.4H5.6a1.4 1.4 0 0 1-1.4-1.4V4.6a1.4 1.4 0 0 1 1.4-1.4Z"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.3"
                  stroke-linejoin="round"
                />
                <path d="M10.6 3.4v4h4" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
              </svg>
            </span>
            <h1 class="share-title" :title="share.meta.name">{{ share.meta.name }}</h1>
            <p class="share-meta">{{ fileMeta }}</p>

            <dl class="facts">
              <div class="fact">
                <dt>有效期</dt>
                <dd>{{ expiryLabel }}</dd>
              </div>
              <div class="fact">
                <dt>剩余下载次数</dt>
                <dd>{{ downloadsLeftLabel }}</dd>
              </div>
            </dl>

            <a v-if="canDownload" class="btn btn-primary btn-block download-button" :href="fileUrl" download>
              <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                <path d="M8 2.6v8.1M4.8 7.5 8 10.7l3.2-3.2" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
                <path d="M3 11.6v1a1.2 1.2 0 0 0 1.2 1.2h7.6a1.2 1.2 0 0 0 1.2-1.2v-1" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
              </svg>
              下载文件
            </a>
            <button v-else class="btn btn-primary btn-block download-button" type="button" disabled>
              {{ downloadBlockLabel }}
            </button>
          </div>

          <div v-else class="dir-card">
            <header class="dir-head">
              <div class="dir-title-wrap">
                <h1 class="share-title" :title="contentTitle">{{ contentTitle }}</h1>
                <p class="share-meta">{{ metaLine }}</p>
              </div>
              <div class="dir-tools">
                <button
                  v-if="!isMulti"
                  class="btn btn-secondary dir-download"
                  type="button"
                  :disabled="!canDownload"
                  :title="canDownload ? `最多打包下载 ${PACK_DOWNLOAD_LIMIT} 个文件` : downloadBlockLabel"
                  @click="downloadAll"
                >
                  <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M8 2.6v8.1M4.8 7.5 8 10.7l3.2-3.2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                    <path d="M3 11.6v1a1.2 1.2 0 0 0 1.2 1.2h7.6a1.2 1.2 0 0 0 1.2-1.2v-1" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
                  </svg>
                  打包下载全部
                </button>
                <span class="dir-badge">{{ isMulti ? '合并分享' : '文件夹分享' }}</span>
              </div>
            </header>

            <nav v-if="crumbs.length > 1" class="crumbs" aria-label="分享内路径">
              <template v-for="(crumb, index) in crumbs" :key="crumb.id ?? 'root'">
                <button
                  v-if="index < crumbs.length - 1"
                  class="crumb-link"
                  type="button"
                  @click="goCrumb(index)"
                >
                  {{ crumb.name }}
                </button>
                <span v-else class="crumb-current" aria-current="page">{{ crumb.name }}</span>
                <span v-if="index < crumbs.length - 1" class="crumb-sep" aria-hidden="true">/</span>
              </template>
            </nav>

            <div v-if="share.entriesLoading" class="list-loading" role="status">
              <span class="loading-dots" aria-hidden="true"><span></span><span></span><span></span></span>
              <p class="loading-text">加载中…</p>
            </div>

            <div v-else-if="share.entriesError" class="list-error" role="alert">
              <p class="state-desc">{{ share.entriesError }}</p>
              <button class="btn btn-secondary" type="button" @click="share.loadNodes(currentCrumb?.id ?? null)">
                重试
              </button>
            </div>

            <p v-else-if="share.entries.length === 0" class="empty-dir">这个文件夹是空的</p>

            <ul v-else class="entry-list">
              <li v-for="node in share.entries" :key="node.id" class="entry">
                <template v-if="node.kind === 'dir'">
                  <button class="entry-main" type="button" :title="node.name" @click="enterDir(node)">
                    <svg class="entry-icon is-dir" width="18" height="18" viewBox="0 0 20 20" aria-hidden="true">
                      <path
                        d="M2.8 6.2A1.6 1.6 0 0 1 4.4 4.6h3.3l1.6 1.9h6.3a1.6 1.6 0 0 1 1.6 1.6v6.7a1.6 1.6 0 0 1-1.6 1.6H4.4a1.6 1.6 0 0 1-1.6-1.6V6.2Z"
                        fill="currentColor"
                        fill-opacity="0.16"
                        stroke="currentColor"
                        stroke-width="1.4"
                        stroke-linejoin="round"
                      />
                    </svg>
                    <span class="entry-name">{{ node.name }}</span>
                    <span class="entry-size">文件夹</span>
                    <svg class="entry-chevron" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                      <path d="M9 6l6 6-6 6" stroke-linecap="round" stroke-linejoin="round" />
                    </svg>
                  </button>
                </template>
                <template v-else>
                  <span class="entry-main is-static">
                    <svg class="entry-icon is-file" width="18" height="18" viewBox="0 0 20 20" aria-hidden="true">
                      <path
                        d="M5.6 3.2h5.2l4 4v9.2a1.4 1.4 0 0 1-1.4 1.4H5.6a1.4 1.4 0 0 1-1.4-1.4V4.6a1.4 1.4 0 0 1 1.4-1.4Z"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="1.4"
                        stroke-linejoin="round"
                      />
                      <path d="M10.6 3.4v4h4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
                    </svg>
                    <span class="entry-name">{{ node.name }}</span>
                    <span class="entry-size">{{ formatBytes(node.size) }}</span>
                  </span>
                  <a
                    v-if="canDownload"
                    class="entry-download"
                    :href="downloadUrlFor(node)"
                    download
                    :aria-label="`下载 ${node.name}`"
                    title="下载"
                  >
                    <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                      <path d="M8 2.6v8.1M4.8 7.5 8 10.7l3.2-3.2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                      <path d="M3 11.6v1a1.2 1.2 0 0 0 1.2 1.2h7.6a1.2 1.2 0 0 0 1.2-1.2v-1" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
                    </svg>
                  </a>
                  <span v-else class="entry-download is-disabled" :title="downloadBlockLabel" aria-hidden="true">
                    <svg width="15" height="15" viewBox="0 0 16 16">
                      <path d="M8 2.6v8.1M4.8 7.5 8 10.7l3.2-3.2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                      <path d="M3 11.6v1a1.2 1.2 0 0 0 1.2 1.2h7.6a1.2 1.2 0 0 0 1.2-1.2v-1" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
                    </svg>
                  </span>
                </template>
              </li>
            </ul>

            <div v-if="canLoadMore && !share.entriesLoading" class="load-more">
              <button class="btn btn-secondary" type="button" @click="share.loadMoreNodes">加载更多</button>
            </div>
          </div>
        </template>
      </main>

      <footer class="public-footer">
        <span>由 Rapig 自托管网盘分享</span>
        <button class="footer-link" type="button" @click="share.open(token)">
          刷新
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.public-page {
  min-height: 100vh;
  display: flex;
  justify-content: center;
  padding: max(26px, 6vh) 16px 48px;
  background: var(--color-bg);
}

.public-shell {
  display: flex;
  width: min(560px, 100%);
  flex-direction: column;
  gap: 14px;
}

.public-shell.is-wide {
  width: min(760px, 100%);
}

.brand {
  display: flex;
  align-items: center;
  gap: 9px;
}

.wordmark {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: -0.015em;
}

.brand-note {
  padding-left: 2px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.public-card {
  padding: 26px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.public-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 2px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.footer-link {
  color: var(--color-muted);
  font-size: var(--fs-xs);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.footer-link:hover {
  color: var(--color-text);
}

.loading-card {
  display: flex;
  align-items: center;
  gap: 10px;
  justify-content: center;
  padding: 26px 0;
}

.loading-text {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.loading-dots {
  display: inline-flex;
  gap: 4px;
}

.loading-dots span {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-primary);
  animation: public-dot 1s ease-in-out infinite;
}

.loading-dots span:nth-child(2) {
  animation-delay: 0.15s;
}

.loading-dots span:nth-child(3) {
  animation-delay: 0.3s;
}

@keyframes public-dot {
  0%,
  100% {
    opacity: 0.35;
    transform: translateY(0);
  }
  50% {
    opacity: 1;
    transform: translateY(-3px);
  }
}

.error-card,
.locked-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 8px 0;
  text-align: center;
}

.state-icon {
  display: grid;
  width: 46px;
  height: 46px;
  margin-bottom: 12px;
  place-items: center;
  border-radius: 50%;
}

.state-icon.is-danger {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.state-icon.is-lock {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.state-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
}

.state-desc {
  margin: 6px 0 0;
  max-width: 380px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  line-height: 1.65;
}

.error-card .btn {
  margin-top: 18px;
}

.error-actions {
  display: flex;
  justify-content: center;
  gap: 8px;
  margin-top: 18px;
}

.error-actions .btn {
  margin-top: 0;
}

.password-form {
  width: min(300px, 100%);
  margin-top: 18px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  text-align: left;
}

.password-row {
  position: relative;
  display: flex;
  align-items: center;
}

.password-row .input {
  padding-right: 38px;
}

.eye-button {
  position: absolute;
  right: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
}

.eye-button:hover {
  color: var(--color-text);
  background: var(--color-bg);
}

.field-error {
  margin: 0;
  color: var(--color-danger);
  font-size: var(--fs-sm);
}

.file-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
}

.file-icon {
  display: grid;
  width: 58px;
  height: 58px;
  margin-bottom: 12px;
  place-items: center;
  border-radius: var(--radius);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.share-title {
  margin: 0;
  max-width: 100%;
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
  overflow-wrap: anywhere;
}

.share-meta {
  margin: 5px 0 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.facts {
  display: flex;
  width: 100%;
  margin: 18px 0;
  border-top: 1px solid var(--color-border);
}

.fact {
  flex: 1;
  padding: 12px 0;
  border-bottom: 1px solid var(--color-border);
}

.fact + .fact {
  border-left: 1px solid var(--color-border);
}

.fact dt {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.fact dd {
  margin: 3px 0 0;
  font-size: var(--fs-sm);
}

.download-button {
  text-decoration: none;
}

.dir-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--color-border);
}

.dir-title-wrap {
  min-width: 0;
}

.dir-badge {
  flex: none;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-size: var(--fs-xs);
}

.dir-tools {
  display: flex;
  flex: none;
  gap: 8px;
  align-items: center;
}

.dir-download {
  height: 30px;
  padding: 0 10px;
  font-size: var(--fs-xs);
}

.dir-download:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.crumbs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
  margin: 12px 0 4px;
  font-size: var(--fs-sm);
}

.crumb-link {
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.crumb-link:hover {
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.crumb-current {
  color: var(--color-text);
  font-weight: 500;
  overflow-wrap: anywhere;
}

.crumb-sep {
  color: var(--color-border-strong);
}

.list-loading {
  display: flex;
  align-items: center;
  gap: 10px;
  justify-content: center;
  padding: 26px 0;
}

.list-error {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 24px 0;
  text-align: center;
}

.empty-dir {
  margin: 0;
  padding: 30px 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  text-align: center;
}

.entry-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.entry {
  display: flex;
  align-items: center;
  gap: 8px;
  border-bottom: 1px solid var(--color-border);
}

.entry:last-child {
  border-bottom: 0;
}

.entry-main {
  display: flex;
  flex: 1;
  min-width: 0;
  align-items: center;
  gap: 10px;
  padding: 11px 0;
  text-align: left;
}

button.entry-main {
  border-radius: var(--radius-sm);
}

button.entry-main:hover .entry-name {
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.entry-main.is-static {
  cursor: default;
}

.entry-icon {
  flex: none;
  color: var(--color-muted);
}

.entry-icon.is-dir {
  color: var(--color-primary);
}

.entry-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--fs-sm);
}

.entry-size {
  margin-left: auto;
  flex: none;
  color: var(--color-muted);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
}

.entry-chevron {
  flex: none;
  color: var(--color-border-strong);
}

.entry-download {
  display: grid;
  flex: none;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
}

.entry-download:hover {
  background: var(--color-bg);
  color: var(--color-primary-ink);
}

.entry-download.is-disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.entry-download.is-disabled:hover {
  background: none;
  color: var(--color-muted);
}

.load-more {
  display: flex;
  justify-content: center;
  padding-top: 12px;
  border-top: 1px solid var(--color-border);
  margin-top: 12px;
}

@media (max-width: 520px) {
  .public-page {
    padding-top: 20px;
  }

  .public-card {
    padding: 20px 18px;
  }

  .facts {
    flex-direction: column;
  }

  .fact + .fact {
    border-left: 0;
  }
}
</style>
