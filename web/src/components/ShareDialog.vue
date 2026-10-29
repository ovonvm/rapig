<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { NodeItem, ShareInfo } from '../api/client'
import { useSharesStore } from '../stores/shares'
import { copyText, selectInputText } from '../lib/clipboard'
import { messageForError } from '../lib/errors'
import { absoluteShareUrl, shareExpiryLabel } from '../lib/shareStatus'

type ExpiryKey = 'hour' | 'day' | 'week' | 'month' | 'permanent'

const props = defineProps<{
  open: boolean
  node: NodeItem | null
  /** Multi-selection mode: 2+ nodes are merged into a single link. */
  nodes?: NodeItem[]
}>()

const emit = defineEmits<{
  close: []
  created: [share: ShareInfo]
}>()

const EXPIRES_OPTIONS: { key: ExpiryKey; label: string; seconds: number | null }[] = [
  { key: 'hour', label: '1 小时', seconds: 3600 },
  { key: 'day', label: '1 天', seconds: 86400 },
  { key: 'week', label: '7 天', seconds: 604800 },
  { key: 'month', label: '30 天', seconds: 2592000 },
  { key: 'permanent', label: '永久', seconds: null },
]

const MAX_DOWNLOADS_LIMIT = 1_000_000

const shares = useSharesStore()

const usePassword = ref(false)
const password = ref('')
const showPassword = ref(false)
const expiresKey = ref<ExpiryKey>('week')
const permanentAck = ref(false)
const maxDownloads = ref<string | number>('')
const submitting = ref(false)
const fieldError = ref<string | null>(null)
const passwordError = ref<string | null>(null)
const maxError = ref<string | null>(null)
const ackError = ref<string | null>(null)
const submitError = ref<string | null>(null)

const result = ref<ShareInfo | null>(null)
const createdUrl = ref('')
const copied = ref(false)
const copyFailed = ref(false)

const dialogRef = ref<HTMLElement | null>(null)
const passwordInput = ref<HTMLInputElement | null>(null)
const linkInput = ref<HTMLInputElement | null>(null)
let copiedTimer: number | null = null

const isPermanent = computed(() => expiresKey.value === 'permanent')
const targets = computed<NodeItem[]>(() => {
  const list = props.nodes ?? []
  if (list.length > 0) return list
  return props.node ? [props.node] : []
})
const isMulti = computed(() => targets.value.length > 1)
const targetCount = computed(() => targets.value.length)
const nodeName = computed(() => props.node?.name ?? '')
const nodeKindLabel = computed(() => (props.node?.kind === 'dir' ? '文件夹' : '文件'))
const targetSummary = computed(() => {
  if (isMulti.value) return `共 ${targetCount.value} 项，合并为一条分享链接`
  if (!props.node) return ''
  return `${nodeKindLabel.value}「${nodeName.value}」`
})
const resultSummary = computed(() => {
  const share = result.value
  if (!share) return ''
  if (share.kind === 'multi') return `共 ${share.item_count ?? targetCount.value} 项`
  return `${share.kind === 'dir' ? '文件夹' : '文件'}「${share.name}」`
})

function resetState(): void {
  usePassword.value = false
  password.value = ''
  showPassword.value = false
  expiresKey.value = 'week'
  permanentAck.value = false
  maxDownloads.value = ''
  submitting.value = false
  fieldError.value = null
  passwordError.value = null
  maxError.value = null
  ackError.value = null
  submitError.value = null
  result.value = null
  createdUrl.value = ''
  copied.value = false
  copyFailed.value = false
}

watch(
  () => props.open,
  (open) => {
    if (!open) {
      document.body.style.overflow = ''
      return
    }

    resetState()
    document.body.style.overflow = 'hidden'
    void nextTick(() => {
      dialogRef.value?.focus()
    })
  },
)

watch(usePassword, (enabled) => {
  if (enabled) void nextTick(() => passwordInput.value?.focus())
})

function clearCopiedTimer(): void {
  if (copiedTimer !== null) {
    window.clearTimeout(copiedTimer)
    copiedTimer = null
  }
}

function validate(): boolean {
  fieldError.value = null
  passwordError.value = null
  maxError.value = null
  ackError.value = null

  if (usePassword.value && password.value.length < 4) {
    passwordError.value = '密码至少需要 4 个字符'
    return false
  }

  if (usePassword.value && Array.from(password.value).length > 64) {
    passwordError.value = '密码不能超过 64 个字符'
    return false
  }

  const raw = String(maxDownloads.value ?? '').trim()
  if (raw !== '') {
    const value = Number(raw)
    if (!Number.isInteger(value) || value < 0 || value > MAX_DOWNLOADS_LIMIT) {
      maxError.value = '下载次数上限需在 0–1000000 之间，留空表示不限'
      return false
    }
  }

  if (isPermanent.value && !permanentAck.value) {
    ackError.value = '请勾选确认后再创建永久链接'
    return false
  }

  return true
}

function resolveExpiresIn(): number | null {
  const option = EXPIRES_OPTIONS.find((item) => item.key === expiresKey.value)
  return option ? option.seconds : null
}

function resolveMaxDownloads(): number {
  const raw = String(maxDownloads.value ?? '').trim()
  if (raw === '') return 0
  return Number(raw)
}

async function create(): Promise<void> {
  if (targets.value.length === 0 || submitting.value) return
  if (!validate()) return

  submitting.value = true
  submitError.value = null

  try {
    const ids = targets.value.map((item) => item.id)
    const share = await shares.create({
      ...(isMulti.value ? { node_ids: ids } : { node_id: ids[0] }),
      password: usePassword.value ? password.value : undefined,
      expires_in: resolveExpiresIn(),
      max_downloads: resolveMaxDownloads(),
    })

    result.value = share
    createdUrl.value = absoluteShareUrl(share)
    emit('created', share)
    await nextTick()
    linkInput.value?.focus()
    linkInput.value?.select()
  } catch (cause) {
    submitError.value = messageForError(cause, '创建分享失败，请稍后重试')
  } finally {
    submitting.value = false
  }
}

async function copyLink(): Promise<void> {
  const text = createdUrl.value
  if (!text) return

  clearCopiedTimer()
  const ok = await copyText(text)
  copied.value = ok
  copyFailed.value = !ok

  if (!ok) selectInputText(linkInput.value)
  copiedTimer = window.setTimeout(() => {
    copied.value = false
    copiedTimer = null
  }, 1800)
}

async function copyAndClose(): Promise<void> {
  await copyLink()
  if (copied.value) close()
}

function close(): void {
  if (submitting.value) return
  clearCopiedTimer()
  emit('close')
}

function onBackdrop(): void {
  close()
}

function focusables(): HTMLElement[] {
  const root = dialogRef.value
  if (!root) return []
  return Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  )
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    close()
    return
  }

  if (event.key !== 'Tab') return

  const items = focusables()
  if (items.length === 0) return

  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement

  if (event.shiftKey && active === first) {
    event.preventDefault()
    last?.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first?.focus()
  }
}

onBeforeUnmount(() => {
  clearCopiedTimer()
  document.body.style.overflow = ''
})
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="open" class="overlay" @mousedown.self="onBackdrop">
        <div
          ref="dialogRef"
          class="dialog"
          role="dialog"
          aria-modal="true"
          tabindex="-1"
          aria-labelledby="share-dialog-title"
          @keydown="onKeydown"
        >
          <header class="dialog-head">
            <h2 id="share-dialog-title" class="dialog-title">
              {{ result ? '分享链接已创建' : isMulti ? `分享 ${targetCount} 项` : '创建分享链接' }}
            </h2>
            <p v-if="!result && targetSummary" class="dialog-sub">{{ targetSummary }}</p>
          </header>

          <template v-if="!result">
            <div class="form">
              <div class="row">
                <div class="row-main">
                  <span class="row-label">密码保护</span>
                  <span class="row-hint">4–64 个字符，打开链接时需要输入</span>
                </div>
                <label class="switch">
                  <input v-model="usePassword" type="checkbox" class="switch-input" />
                  <span class="switch-track" aria-hidden="true"><span class="switch-thumb" /></span>
                </label>
              </div>

              <div v-if="usePassword" class="field">
                <label class="field-label" for="share-password">分享密码</label>
                <div class="password-row">
                  <input
                    id="share-password"
                    ref="passwordInput"
                    v-model="password"
                    class="input password-input"
                    :type="showPassword ? 'text' : 'password'"
                    autocomplete="new-password"
                    maxlength="64"
                    placeholder="输入 4–64 个字符"
                    :aria-invalid="Boolean(passwordError)"
                    :aria-describedby="passwordError ? 'share-password-error' : undefined"
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
                <p v-if="passwordError" id="share-password-error" class="field-error">{{ passwordError }}</p>
              </div>

              <div class="field">
                <label class="field-label" for="share-expires">有效期</label>
                <div class="select-wrap">
                  <select id="share-expires" v-model="expiresKey" class="input select">
                    <option v-for="option in EXPIRES_OPTIONS" :key="option.key" :value="option.key">
                      {{ option.label }}
                    </option>
                  </select>
                  <svg class="select-arrow" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </div>
              </div>

              <div v-if="isPermanent" class="field">
                <label class="check-row">
                  <input v-model="permanentAck" type="checkbox" class="check" />
                  <span>我明白永久链接不会自动回收</span>
                </label>
                <p v-if="ackError" class="field-error">{{ ackError }}</p>
              </div>

              <div class="field">
                <label class="field-label" for="share-max">下载次数上限</label>
                <input
                  id="share-max"
                  v-model="maxDownloads"
                  class="input"
                  type="number"
                  min="0"
                  :max="MAX_DOWNLOADS_LIMIT"
                  inputmode="numeric"
                  placeholder="留空表示不限"
                  :aria-invalid="Boolean(maxError)"
                  :aria-describedby="maxError ? 'share-max-error' : undefined"
                />
                <p v-if="maxError" id="share-max-error" class="field-error">{{ maxError }}</p>
                <p v-else class="field-hint">0 也表示不限次数</p>
              </div>

              <p v-if="submitError" class="form-error" role="alert">{{ submitError }}</p>
            </div>

            <footer class="dialog-actions">
              <button type="button" class="btn btn-secondary" :disabled="submitting" @click="close">取消</button>
              <button type="button" class="btn btn-primary" :disabled="submitting || targets.length === 0" @click="create">
                {{ submitting ? '创建中…' : isMulti ? '创建合并链接' : '创建链接' }}
              </button>
            </footer>
          </template>

          <template v-else>
            <div class="form">
              <div class="field">
                <label class="field-label" for="share-link">分享链接</label>
                <div class="link-row">
                  <input id="share-link" ref="linkInput" class="input link-input" :value="createdUrl" readonly @focus="($event.target as HTMLInputElement).select()" />
                  <button type="button" class="btn btn-secondary copy-button" @click="copyLink">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true">
                      <rect x="9" y="9" width="11" height="11" rx="2" />
                      <path d="M15 5.5A2.5 2.5 0 0012.5 3H6a2.5 2.5 0 00-2.5 2.5V13" />
                    </svg>
                    {{ copied ? '已复制' : '复制链接' }}
                  </button>
                </div>
                <p v-if="copyFailed" class="field-hint copy-hint">浏览器阻止了自动复制，链接已全选，请按 Ctrl+C 手动复制</p>
                <p v-else-if="result.kind === 'multi'" class="field-hint">
                  该链接包含 {{ result.item_count ?? targetCount }} 项；拿到链接的人会看到这些内容的列表。
                </p>
                <p v-else class="field-hint">拿到链接的人可以访问；撤销后会立即失效。</p>
              </div>

              <dl class="summary">
                <div class="summary-row">
                  <dt>对象</dt>
                  <dd>{{ resultSummary }}</dd>
                </div>
                <div class="summary-row">
                  <dt>密码</dt>
                  <dd>{{ result.has_password ? '已设置' : '未设置' }}</dd>
                </div>
                <div class="summary-row">
                  <dt>有效期</dt>
                  <dd>{{ shareExpiryLabel(result.expires_at) }}</dd>
                </div>
                <div class="summary-row">
                  <dt>次数上限</dt>
                  <dd>{{ result.max_downloads > 0 ? `${result.max_downloads} 次` : '不限' }}</dd>
                </div>
              </dl>
            </div>

            <footer class="dialog-actions">
              <button type="button" class="btn btn-secondary" @click="close">关闭</button>
              <button type="button" class="btn btn-primary" @click="copyAndClose">复制并关闭</button>
            </footer>
          </template>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: var(--color-overlay);
}

.dialog {
  width: min(440px, 100%);
  max-height: min(88vh, 640px);
  overflow-y: auto;
  padding: 22px 22px 18px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  outline: none;
}

.dialog-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.dialog-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
}

.dialog-sub {
  margin: 0;
  max-width: 100%;
  overflow: hidden;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.form {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-top: 16px;
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 18px;
}

.dialog-enter-active,
.dialog-leave-active {
  transition: opacity 160ms var(--ease);
}

.dialog-enter-active .dialog,
.dialog-leave-active .dialog {
  transition:
    opacity 160ms var(--ease),
    transform 160ms var(--ease);
}

.dialog-enter-from,
.dialog-leave-to {
  opacity: 0;
}

.dialog-enter-from .dialog,
.dialog-leave-to .dialog {
  opacity: 0;
  transform: translateY(6px) scale(0.99);
}

.row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.row-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.row-label {
  font-size: var(--fs-sm);
  font-weight: 500;
}

.row-hint {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.switch {
  position: relative;
  display: inline-flex;
  flex: none;
  width: 38px;
  height: 22px;
  cursor: pointer;
}

.switch-input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}

.switch-track {
  position: absolute;
  inset: 0;
  border-radius: var(--radius-pill);
  background: var(--color-border-strong);
  transition: background var(--dur) var(--ease);
}

.switch-thumb {
  position: absolute;
  top: 3px;
  left: 3px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--color-surface);
  transition: transform var(--dur) var(--ease);
}

.switch-input:checked + .switch-track {
  background: var(--color-primary);
}

.switch-input:checked + .switch-track .switch-thumb {
  transform: translateX(16px);
}

.switch-input:focus-visible + .switch-track {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.password-row {
  position: relative;
  display: flex;
  align-items: center;
}

.password-input {
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

.select-wrap {
  position: relative;
  display: flex;
  align-items: center;
}

.select {
  width: 100%;
  padding-right: 32px;
  appearance: none;
  cursor: pointer;
}

.select-arrow {
  position: absolute;
  right: 11px;
  color: var(--color-muted);
  pointer-events: none;
}

.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  cursor: pointer;
}

.check {
  width: 15px;
  height: 15px;
  margin: 0;
  accent-color: var(--color-primary);
  cursor: pointer;
}

.form-error {
  margin: 0;
  padding: 9px 11px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-soft);
  color: var(--color-danger);
  font-size: var(--fs-sm);
}

.link-row {
  display: flex;
  gap: 8px;
}

.link-input {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Consolas, monospace);
  font-size: var(--fs-xs);
  color: var(--color-primary-ink);
  background: var(--color-primary-soft);
  border-color: var(--color-border-strong);
}

.copy-button {
  flex: none;
}

.copy-hint {
  color: var(--color-danger);
}

.summary {
  display: flex;
  flex-direction: column;
  gap: 0;
  margin: 0;
  border-top: 1px solid var(--color-border);
}

.summary-row {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  padding: 9px 0;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-sm);
}

.summary-row:last-child {
  border-bottom: 0;
}

.summary-row dt {
  color: var(--color-muted);
}

.summary-row dd {
  margin: 0;
  text-align: right;
  word-break: break-all;
}

@media (max-width: 460px) {
  .link-row {
    flex-direction: column;
  }

  .copy-button {
    width: 100%;
  }
}
</style>
