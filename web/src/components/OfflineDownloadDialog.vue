<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { TransferItem } from '../api/client'
import { messageForError } from '../lib/errors'
import { formatBytes } from '../lib/format'
import { validateTorrentFile } from '../lib/torrent'
import { validateMagnet, validateTransferUrl } from '../lib/url'
import { useTransfersStore } from '../stores/transfers'

type LinkMode = 'http' | 'qb' | 'torrent'

const props = withDefaults(
  defineProps<{
    open: boolean
    /** Destination folder id; 0 = drive root. */
    parent: number
    /** Shown in the subtitle so the user knows where the file lands. */
    folderLabel?: string
    /** Only when the server reports qBittorrent configured. */
    allowMagnet?: boolean
    /** Only when the server reports qBittorrent configured. */
    allowTorrent?: boolean
  }>(),
  { folderLabel: '我的文件', allowMagnet: false, allowTorrent: false },
)

const emit = defineEmits<{ close: []; created: [transfer: TransferItem] }>()

const transfers = useTransfersStore()

const dialogRef = ref<HTMLElement | null>(null)
const urlInput = ref<HTMLInputElement | null>(null)
const magnetInput = ref<HTMLTextAreaElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const fileButton = ref<HTMLButtonElement | null>(null)
const mode = ref<LinkMode>('http')
const url = ref('')
const magnet = ref('')
const torrentFile = ref<File | null>(null)
const name = ref('')
const linkError = ref<string | null>(null)
const fileError = ref<string | null>(null)
const submitError = ref<string | null>(null)
const submitting = ref(false)

const canSubmit = computed(() => !submitting.value)
const isMagnet = computed(() => mode.value === 'qb')
const isTorrent = computed(() => mode.value === 'torrent')
const fileSizeText = computed(() => (torrentFile.value ? formatBytes(torrentFile.value.size) : ''))
// Fixed copy: `formatBytes(20 MiB)` would read 「21.0 MB」 and confuse the limit.
const fileLimitText = '20 MB'
const showModeSwitch = computed(() => props.allowMagnet || props.allowTorrent)

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      document.body.style.overflow = ''
      return
    }

    mode.value = 'http'
    url.value = ''
    magnet.value = ''
    torrentFile.value = null
    name.value = ''
    linkError.value = null
    fileError.value = null
    submitError.value = null
    submitting.value = false
    if (fileInput.value) fileInput.value.value = ''
    document.body.style.overflow = 'hidden'
    await nextTick()
    urlInput.value?.focus()
  },
)

// The server can report qBittorrent unavailable after the dialog opened.
watch(
  [() => props.allowMagnet, () => props.allowTorrent],
  ([magnet, torrent]) => {
    if (mode.value === 'qb' && !magnet) mode.value = 'http'
    if (mode.value === 'torrent' && !torrent) mode.value = 'http'
  },
)

onBeforeUnmount(() => {
  document.body.style.overflow = ''
})

function requestClose(): void {
  if (submitting.value) return
  emit('close')
}

function resetErrors(): void {
  linkError.value = null
  fileError.value = null
  submitError.value = null
}

async function selectMode(next: LinkMode): Promise<void> {
  if (submitting.value || mode.value === next) return
  mode.value = next
  resetErrors()
  await nextTick()
  if (next === 'qb') magnetInput.value?.focus()
  else if (next === 'torrent') fileButton.value?.focus()
  else urlInput.value?.focus()
}

function onLinkInput(): void {
  linkError.value = null
  submitError.value = null
}

function chooseTorrent(): void {
  if (submitting.value) return
  fileInput.value?.click()
}

function onFileChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const picked = input.files?.[0] ?? null
  // Reset the input so choosing the same file again still fires `change`.
  input.value = ''
  submitError.value = null

  const check = validateTorrentFile(picked)
  if (!check.ok) {
    torrentFile.value = null
    fileError.value = check.message
    return
  }

  fileError.value = null
  torrentFile.value = picked
}

function clearTorrent(): void {
  torrentFile.value = null
  fileError.value = null
  submitError.value = null
  fileButton.value?.focus()
}

async function submit(): Promise<void> {
  if (submitting.value) return

  if (isTorrent.value) {
    const check = validateTorrentFile(torrentFile.value)
    if (!check.ok || !torrentFile.value) {
      fileError.value = check.ok ? '请选择种子文件' : check.message
      return
    }

    fileError.value = null
    submitError.value = null
    submitting.value = true

    try {
      const transfer = await transfers.createTorrent(torrentFile.value, {
        parent: props.parent,
        name: name.value.trim() || undefined,
      })
      emit('created', transfer)
    } catch (cause) {
      submitError.value = messageForError(cause, '创建离线下载失败，请稍后重试')
    } finally {
      submitting.value = false
    }
    return
  }

  if (isMagnet.value) {
    const check = validateMagnet(magnet.value)
    if (!check.ok) {
      linkError.value = check.message
      magnetInput.value?.focus()
      return
    }

    linkError.value = null
    submitError.value = null
    submitting.value = true

    try {
      const transfer = await transfers.create({
        parent: props.parent,
        kind: 'qb',
        magnet: check.url,
        name: name.value.trim() || undefined,
      })
      emit('created', transfer)
    } catch (cause) {
      submitError.value = messageForError(cause, '创建离线下载失败，请稍后重试')
    } finally {
      submitting.value = false
    }
    return
  }

  const check = validateTransferUrl(url.value)
  if (!check.ok) {
    linkError.value = check.message
    urlInput.value?.focus()
    return
  }

  linkError.value = null
  submitError.value = null
  submitting.value = true

  try {
    const transfer = await transfers.create({
      parent: props.parent,
      // `kind` defaults to http server-side, so older backends keep working.
      url: check.url,
      name: name.value.trim() || undefined,
    })
    emit('created', transfer)
  } catch (cause) {
    submitError.value = messageForError(cause, '创建离线下载失败，请稍后重试')
  } finally {
    submitting.value = false
  }
}

/** Escape closes, Tab stays inside the dialog. */
function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    requestClose()
    return
  }

  if (event.key !== 'Tab') return

  const root = dialogRef.value
  if (!root) return
  const focusable = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), textarea:not([disabled])',
    ),
  ).filter((node) => node.offsetParent !== null)
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (!first || !last) return

  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="open" class="overlay" @mousedown.self="requestClose" @keydown="onKeydown">
        <div
          ref="dialogRef"
          class="dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="offline-dialog-title"
        >
          <form novalidate @submit.prevent="submit">
            <h2 id="offline-dialog-title" class="dialog-title">离线下载</h2>
            <p class="dialog-sub">
              <template v-if="isTorrent">
                上传 .torrent 种子文件，由服务器转交给 qBittorrent 下载，完成后保存到「{{ folderLabel }}」。
              </template>
              <template v-else-if="isMagnet">由服务器转发到 qBittorrent 下载，完成后保存到「{{ folderLabel }}」。</template>
              <template v-else>由服务器从链接下载文件，保存到「{{ folderLabel }}」。</template>
            </p>

            <div v-if="showModeSwitch" class="field">
              <span class="field-label" id="offline-mode-label">链接类型</span>
              <div class="mode-switch" role="group" aria-labelledby="offline-mode-label">
                <button
                  class="mode-option"
                  :class="{ 'is-active': mode === 'http' }"
                  type="button"
                  :aria-pressed="mode === 'http'"
                  @click="selectMode('http')"
                >
                  HTTP 链接
                </button>
                <button
                  v-if="allowMagnet"
                  class="mode-option"
                  :class="{ 'is-active': mode === 'qb' }"
                  type="button"
                  :aria-pressed="mode === 'qb'"
                  @click="selectMode('qb')"
                >
                  磁力链接
                </button>
                <button
                  v-if="allowTorrent"
                  class="mode-option"
                  :class="{ 'is-active': mode === 'torrent' }"
                  type="button"
                  :aria-pressed="mode === 'torrent'"
                  @click="selectMode('torrent')"
                >
                  种子文件
                </button>
              </div>
            </div>

            <div v-if="isTorrent" class="field">
              <span class="field-label">种子文件</span>
              <input
                ref="fileInput"
                class="torrent-input"
                type="file"
                accept=".torrent,application/x-bittorrent"
                tabindex="-1"
                aria-hidden="true"
                @change="onFileChange"
              />
              <div v-if="torrentFile" class="file-chip">
                <svg class="file-chip-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <path
                    d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5Z"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linejoin="round"
                  />
                  <path d="M14 3v5h5" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" />
                </svg>
                <span class="file-chip-name" :title="torrentFile.name">{{ torrentFile.name }}</span>
                <span class="file-chip-size">{{ fileSizeText }}</span>
                <button class="file-chip-clear" type="button" aria-label="清除已选文件" @click="clearTorrent">
                  清除
                </button>
              </div>
              <div v-else class="file-pick">
                <button ref="fileButton" class="btn btn-secondary file-pick-button" type="button" @click="chooseTorrent">
                  <svg class="file-pick-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                    <path
                      d="M12 16V4m0 0 4 4m-4-4L8 8"
                      stroke="currentColor"
                      stroke-width="1.7"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    />
                    <path
                      d="M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"
                      stroke="currentColor"
                      stroke-width="1.7"
                      stroke-linecap="round"
                    />
                  </svg>
                  选择 .torrent 文件
                </button>
              </div>
              <p v-if="fileError" id="offline-file-error" class="field-error" role="alert">{{ fileError }}</p>
              <p v-else id="offline-file-hint" class="field-hint">
                仅支持 .torrent 文件，最大 {{ fileLimitText }}；由 qBittorrent 下载。
              </p>
            </div>

            <div v-else-if="isMagnet" class="field">
              <label class="field-label" for="offline-magnet">磁力链接</label>
              <textarea
                id="offline-magnet"
                ref="magnetInput"
                v-model="magnet"
                class="input magnet-input"
                name="transfer-magnet"
                rows="3"
                autocomplete="off"
                spellcheck="false"
                placeholder="magnet:?xt=urn:btih:..."
                :aria-invalid="linkError ? 'true' : undefined"
                :aria-describedby="linkError ? 'offline-link-error' : 'offline-magnet-hint'"
                @input="onLinkInput"
              ></textarea>
              <p v-if="linkError" id="offline-link-error" class="field-error" role="alert">{{ linkError }}</p>
              <p v-else id="offline-magnet-hint" class="field-hint">
                以 magnet:?xt=urn:btih: 开头的磁力链接，由 qBittorrent 下载。
              </p>
            </div>

            <div v-else class="field">
              <label class="field-label" for="offline-url">链接</label>
              <input
                id="offline-url"
                ref="urlInput"
                v-model="url"
                class="input"
                type="url"
                name="transfer-url"
                inputmode="url"
                autocomplete="off"
                spellcheck="false"
                placeholder="https://example.com/file.zip"
                :aria-invalid="linkError ? 'true' : undefined"
                :aria-describedby="linkError ? 'offline-link-error' : 'offline-url-hint'"
                @input="onLinkInput"
              />
              <p v-if="linkError" id="offline-link-error" class="field-error" role="alert">{{ linkError }}</p>
              <p v-else id="offline-url-hint" class="field-hint">
                仅支持公网 HTTP/HTTPS 地址，私网与回环地址会被拒绝。
              </p>
            </div>

            <div class="field">
              <label class="field-label" for="offline-name">
                {{ isMagnet || isTorrent ? '任务名称（可选）' : '文件名（可选）' }}
              </label>
              <input
                id="offline-name"
                v-model="name"
                class="input"
                type="text"
                name="transfer-name"
                maxlength="255"
                autocomplete="off"
                spellcheck="false"
                :placeholder="
                  isMagnet || isTorrent ? '留空则由 qBittorrent 或种子决定' : '留空则从链接推断'
                "
              />
              <p class="field-hint">
                {{ isMagnet || isTorrent ? '仅影响网盘中的显示名称。' : '留空则由服务器从链接推断文件名。' }}
              </p>
            </div>

            <p v-if="submitError" class="submit-error" role="alert">{{ submitError }}</p>

            <div class="dialog-actions">
              <button class="btn btn-secondary" type="button" :disabled="submitting" @click="requestClose">
                取消
              </button>
              <button class="btn btn-primary" type="submit" :disabled="!canSubmit">
                {{ submitting ? '创建中…' : '开始下载' }}
              </button>
            </div>
          </form>
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
  width: min(460px, 100%);
  max-height: min(88vh, 720px);
  overflow-y: auto;
  padding: 22px 22px 18px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.dialog-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
}

.dialog-sub {
  margin: 6px 0 16px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.field + .field {
  margin-top: 14px;
}

.mode-switch {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.mode-option {
  padding: 4px 12px;
  border: 0;
  border-radius: calc(var(--radius-sm) - 2px);
  background: transparent;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.mode-option:hover {
  color: var(--color-text);
}

.mode-option.is-active {
  background: var(--color-surface);
  color: var(--color-primary-ink);
  box-shadow: 0 0 0 1px var(--color-border-strong);
}

.magnet-input {
  min-height: 96px;
  padding: 9px 11px;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: var(--fs-xs);
  line-height: 1.5;
  color: var(--color-text);
  overflow-wrap: anywhere;
  resize: vertical;
}

.torrent-input {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
  border: 0;
}

.file-pick {
  display: flex;
}

.file-pick-button {
  gap: 7px;
}

.file-pick-icon {
  width: 15px;
  height: 15px;
  color: var(--color-primary-ink);
}

.file-chip {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 8px 10px;
  background: var(--color-primary-soft);
  border: 1px solid color-mix(in srgb, var(--color-primary) 30%, var(--color-border));
  border-radius: var(--radius-sm);
}

.file-chip-icon {
  flex: none;
  width: 18px;
  height: 18px;
  color: var(--color-primary-ink);
}

.file-chip-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  font-size: var(--fs-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-chip-size {
  flex: none;
  font-size: var(--fs-xs);
  color: var(--color-muted);
  font-variant-numeric: tabular-nums;
}

.file-chip-clear {
  flex: none;
  padding: 0 2px;
  border: 0;
  background: none;
  font-size: var(--fs-xs);
  color: var(--color-primary-ink);
}

.file-chip-clear:hover {
  text-decoration: underline;
}

.submit-error {
  margin-top: 12px;
  font-size: var(--fs-sm);
  color: var(--color-danger);
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
</style>
