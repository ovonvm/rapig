<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { NodeItem } from '../api/client'
import { downloadUrl } from '../api/client'
import { formatBytes } from '../lib/format'
import {
  fetchTextPreview,
  previewKind,
  previewKindLabel,
  type PreviewKind,
  type TextPreview,
} from '../lib/preview'

const props = defineProps<{
  open: boolean
  /** Previewable files of the current directory, in list order. */
  files: NodeItem[]
  fileId: number | null
}>()

const emit = defineEmits<{
  close: []
  navigate: [id: number]
  download: [file: NodeItem]
}>()

const closeRef = ref<HTMLButtonElement | null>(null)
const overlayRef = ref<HTMLElement | null>(null)

const index = computed(() => props.files.findIndex((item) => item.id === props.fileId))
const file = computed<NodeItem | null>(() => (index.value >= 0 ? (props.files[index.value] ?? null) : null))
const kind = computed<PreviewKind>(() => (file.value ? previewKind(file.value.name) : null))
const contentUrl = computed(() => (file.value ? downloadUrl(file.value.id) : ''))
const metaText = computed(() =>
  file.value ? `${previewKindLabel(kind.value)} · ${formatBytes(file.value.size)}` : '',
)
const hasPrev = computed(() => index.value > 0)
const hasNext = computed(() => index.value >= 0 && index.value < props.files.length - 1)
const stageClass = computed(() => {
  if (kind.value === 'image' || kind.value === 'video') return 'is-dark'
  if (kind.value === 'text' || kind.value === 'pdf') return 'is-paper'
  return 'is-plain'
})

const imageError = ref(false)
const videoError = ref(false)
const textState = ref<TextPreview | null>(null)
const textLoading = ref(false)

let restoreFocus: HTMLElement | null = null
let previousOverflow = ''

async function loadText(): Promise<void> {
  const current = file.value
  if (!props.open || !current || kind.value !== 'text') {
    textState.value = null
    textLoading.value = false
    return
  }

  textLoading.value = true
  textState.value = null
  try {
    textState.value = await fetchTextPreview(downloadUrl(current.id))
  } finally {
    textLoading.value = false
  }
}

watch(
  [() => props.open, () => file.value?.id],
  () => {
    imageError.value = false
    videoError.value = false
    void loadText()
  },
  { immediate: true },
)

function goPrev(): void {
  if (!hasPrev.value) return
  const target = props.files[index.value - 1]
  if (target) emit('navigate', target.id)
}

function goNext(): void {
  if (!hasNext.value) return
  const target = props.files[index.value + 1]
  if (target) emit('navigate', target.id)
}

/** Keep Tab inside the dialog, like the other dialogs in the app. */
function trapTab(event: KeyboardEvent): void {
  if (event.key !== 'Tab') return
  const root = overlayRef.value
  if (!root) return

  const focusable = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), a[href], input:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((element) => element.offsetParent !== null)
  if (focusable.length === 0) return

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

watch(
  () => props.open,
  async (open) => {
    if (open) {
      restoreFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
      previousOverflow = document.body.style.overflow
      document.body.style.overflow = 'hidden'
      await nextTick()
      closeRef.value?.focus()
      return
    }

    document.body.style.overflow = previousOverflow
    restoreFocus?.focus()
    restoreFocus = null
  },
)

onBeforeUnmount(() => {
  document.body.style.overflow = previousOverflow
})
</script>

<template>
  <Teleport to="body">
    <Transition name="preview">
      <div
        v-if="open && file"
        ref="overlayRef"
        class="overlay"
        role="dialog"
        aria-modal="true"
        :aria-label="`预览 ${file.name}`"
        @click.self="emit('close')"
        @keydown="trapTab"
      >
        <section class="preview">
          <header class="bar">
            <div class="bar-main">
              <p class="bar-name" :title="file.name">{{ file.name }}</p>
              <p class="bar-meta">{{ metaText }}</p>
            </div>

            <div class="bar-actions">
              <button
                class="icon-button"
                type="button"
                :disabled="!hasPrev"
                aria-label="上一个文件"
                title="上一个文件"
                @click="goPrev"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                  <path
                    d="M10 3.4 5.4 8l4.6 4.6"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                </svg>
              </button>
              <button
                class="icon-button"
                type="button"
                :disabled="!hasNext"
                aria-label="下一个文件"
                title="下一个文件"
                @click="goNext"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                  <path
                    d="M6 3.4 10.6 8 6 12.6"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                </svg>
              </button>
              <span class="bar-divider" aria-hidden="true"></span>
              <button
                ref="closeRef"
                class="icon-button close"
                type="button"
                aria-label="关闭预览"
                title="关闭"
                @click="emit('close')"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                  <path
                    d="m4 4 8 8M12 4l-8 8"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linecap="round"
                  />
                </svg>
              </button>
            </div>
          </header>

          <div class="stage" :class="stageClass" @click.stop>
            <template v-if="kind === 'image'">
              <img
                v-if="!imageError"
                class="image"
                :src="contentUrl"
                :alt="file.name"
                @error="imageError = true"
              />
              <div v-else class="fallback">
                <p class="fallback-title">图片加载失败</p>
                <p class="fallback-desc">文件可能已被移动或删除，可以下载后再查看。</p>
                <button class="btn btn-secondary" type="button" @click="emit('download', file)">
                  下载文件
                </button>
              </div>
            </template>

            <template v-else-if="kind === 'pdf'">
              <embed class="pdf" :src="contentUrl" type="application/pdf" />
            </template>

            <template v-else-if="kind === 'text'">
              <div v-if="textLoading" class="state">
                <p class="state-title">正在加载文本…</p>
              </div>
              <div v-else-if="textState?.error" class="state">
                <p class="state-title">文本加载失败</p>
                <p class="state-desc">{{ textState.error }}</p>
                <button class="btn btn-secondary" type="button" @click="emit('download', file)">
                  下载文件
                </button>
              </div>
              <div v-else-if="textState?.binary" class="state">
                <p class="state-title">这是二进制文件，无法以文本预览</p>
                <p class="state-desc">下载后用对应的程序打开。</p>
                <button class="btn btn-secondary" type="button" @click="emit('download', file)">
                  下载文件
                </button>
              </div>
              <template v-else-if="textState">
                <pre class="text">{{ textState.text }}</pre>
                <p v-if="textState.truncated" class="text-note">
                  仅显示前 1 MiB，完整内容请下载
                </p>
              </template>
            </template>

            <template v-else>
              <div class="unsupported">
                <span class="unsupported-icon" aria-hidden="true">
                  <svg width="46" height="46" viewBox="0 0 24 24">
                    <path
                      d="M6 3.2h7.2L18 8v12.2a1.6 1.6 0 0 1-1.6 1.6H6a1.6 1.6 0 0 1-1.6-1.6V4.8A1.6 1.6 0 0 1 6 3.2Z"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.4"
                      stroke-linejoin="round"
                    />
                    <path d="M13 3.4V8.3h4.7" fill="none" stroke="currentColor" stroke-width="1.4" />
                  </svg>
                </span>
                <p class="unsupported-title">该类型暂不支持预览</p>
                <p class="unsupported-name" :title="file.name">{{ file.name }}</p>
                <p class="unsupported-meta">{{ formatBytes(file.size) }}</p>
                <button class="btn btn-primary" type="button" @click="emit('download', file)">
                  下载文件
                </button>
              </div>
            </template>
          </div>

          <footer class="foot">
            <button class="btn btn-primary" type="button" @click="emit('download', file)">
              下载
            </button>
          </footer>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  z-index: 80;
  display: grid;
  place-items: center;
  padding: 28px;
  background: rgba(12, 10, 9, 0.74);
  backdrop-filter: blur(3px);
}

.preview {
  display: flex;
  width: min(1080px, 100%);
  height: min(86vh, 100%);
  flex-direction: column;
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 14px 12px 18px;
  border-bottom: 1px solid var(--color-border);
}

.bar-main {
  min-width: 0;
}

.bar-name {
  overflow: hidden;
  font-size: var(--fs-base);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bar-meta {
  margin-top: 1px;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.bar-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 4px;
}

.bar-divider {
  width: 1px;
  height: 18px;
  margin: 0 4px;
  background: var(--color-border);
}

.icon-button {
  display: grid;
  width: 30px;
  height: 30px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
  transition:
    background-color var(--dur) var(--ease),
    color var(--dur) var(--ease);
}

.icon-button:hover:not(:disabled) {
  background: var(--color-bg);
  color: var(--color-text);
}

.icon-button:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.icon-button.close:hover {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.stage {
  position: relative;
  flex: 1;
  min-height: 0;
  overflow: auto;
}

.stage.is-dark {
  display: grid;
  place-items: center;
  background: #0c0a09;
  padding: 18px;
}

.stage.is-paper {
  background: var(--color-surface);
}

.stage.is-plain {
  display: grid;
  place-items: center;
  padding: 24px;
  background: var(--color-bg);
}

.image {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}

.media {
  display: grid;
  width: 100%;
  height: 100%;
  place-items: center;
}

.video {
  max-width: 100%;
  max-height: 100%;
  background: #000;
}

.audio-card {
  display: flex;
  min-width: min(360px, 100%);
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 34px 40px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.audio-icon {
  display: grid;
  width: 62px;
  height: 62px;
  place-items: center;
  color: var(--color-primary);
  background: var(--color-primary-soft);
  border-radius: var(--radius);
}

.audio-name {
  max-width: 300px;
  overflow: hidden;
  font-size: var(--fs-base);
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.audio {
  width: 100%;
  min-width: 240px;
}

.audio-hint {
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.pdf {
  display: block;
  width: 100%;
  height: 100%;
  border: 0;
}

.text {
  min-height: 100%;
  margin: 0;
  padding: 20px 22px;
  color: var(--color-text);
  font-family: ui-monospace, 'SFMono-Regular', 'Cascadia Mono', Consolas, monospace;
  font-size: 13px;
  line-height: 1.72;
  tab-size: 2;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.text-note {
  position: sticky;
  bottom: 0;
  padding: 8px 22px;
  background: var(--color-primary-soft);
  border-top: 1px solid var(--color-border);
  color: var(--color-primary-ink);
  font-size: var(--fs-xs);
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 56px 24px;
  text-align: center;
}

.state-title {
  font-size: 15px;
  font-weight: 600;
}

.state-desc {
  max-width: 360px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.fallback {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 32px;
  color: var(--color-text);
  text-align: center;
}

.fallback-title {
  font-size: 15px;
  font-weight: 600;
}

.fallback-desc {
  max-width: 360px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.unsupported {
  display: flex;
  width: min(420px, 100%);
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 38px 34px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  text-align: center;
}

.unsupported-icon {
  display: grid;
  width: 76px;
  height: 76px;
  margin-bottom: 4px;
  place-items: center;
  color: var(--color-muted);
  background: var(--color-bg);
  border-radius: 50%;
}

.unsupported-title {
  font-size: var(--fs-lg);
  font-weight: 600;
}

.unsupported-name {
  max-width: 100%;
  overflow: hidden;
  color: var(--color-text);
  font-size: var(--fs-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.unsupported-meta {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 14px 10px 18px;
  border-top: 1px solid var(--color-border);
}

.foot-hint {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.preview-enter-active,
.preview-leave-active {
  transition: opacity 150ms var(--ease);
}

.preview-enter-active .preview,
.preview-leave-active .preview {
  transition: transform 150ms var(--ease);
}

.preview-enter-from,
.preview-leave-to {
  opacity: 0;
}

.preview-enter-from .preview,
.preview-leave-to .preview {
  transform: scale(0.98);
}

@media (max-width: 700px) {
  .overlay {
    padding: 0;
  }

  .preview {
    width: 100%;
    height: 100%;
    border: 0;
    border-radius: 0;
  }

  .bar {
    padding: 10px 8px 10px 14px;
  }

  .bar-name {
    font-size: var(--fs-sm);
  }

  .foot-hint {
    display: none;
  }

  .foot {
    justify-content: flex-end;
  }

  .audio-card {
    padding: 26px 20px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .preview-enter-active,
  .preview-leave-active,
  .preview-enter-active .preview,
  .preview-leave-active .preview {
    transition: none;
  }
}
</style>
