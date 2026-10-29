<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { NodeItem } from '../api/client'
import { useFilesStore } from '../stores/files'
import { errorCodeOf, messageForError } from '../lib/errors'
import { FOLDER_NAME_HINT, validateNodeName } from '../lib/validation'

const props = defineProps<{
  open: boolean
  node: NodeItem | null
  /**
   * Optional rename implementation. Personal files go through the files store;
   * group spaces (P1) pass their own mutation because the store is scoped to
   * the personal drive.
   */
  rename?: (node: NodeItem, name: string) => Promise<NodeItem>
}>()
const emit = defineEmits<{ close: []; renamed: [node: NodeItem]; refresh: [] }>()

const files = useFilesStore()

const dialogRef = ref<HTMLElement | null>(null)
const inputRef = ref<HTMLInputElement | null>(null)
const name = ref('')
const fieldError = ref<string | null>(null)
const submitError = ref<string | null>(null)
const stale = ref(false)
const submitting = ref(false)

const canSubmit = computed(() => !submitting.value)

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      document.body.style.overflow = ''
      return
    }

    name.value = props.node?.name ?? ''
    fieldError.value = null
    submitError.value = null
    stale.value = false
    submitting.value = false
    document.body.style.overflow = 'hidden'
    await nextTick()
    inputRef.value?.focus()
    inputRef.value?.select()
  },
)

onBeforeUnmount(() => {
  document.body.style.overflow = ''
})

function requestClose(): void {
  if (submitting.value) return
  emit('close')
}

function onInput(): void {
  fieldError.value = null
  submitError.value = null
  stale.value = false
}

async function submit(): Promise<void> {
  if (submitting.value) return
  const node = props.node
  if (!node) return

  const validation = validateNodeName(name.value)
  if (validation) {
    fieldError.value = validation
    inputRef.value?.focus()
    return
  }

  const nextName = name.value.trim()
  if (nextName === node.name) {
    emit('close')
    return
  }

  fieldError.value = null
  submitError.value = null
  stale.value = false
  submitting.value = true

  try {
    const updated = props.rename ? await props.rename(node, nextName) : await files.renameNode(node, nextName)
    emit('renamed', updated)
  } catch (cause) {
    submitError.value = messageForError(cause, '改名失败，请稍后重试')
    stale.value = errorCodeOf(cause) === 'stale_write'
  } finally {
    submitting.value = false
  }
}

function refreshList(): void {
  emit('refresh')
  emit('close')
}

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
    root.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])'),
  )
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
        <div ref="dialogRef" class="dialog" role="dialog" aria-modal="true" aria-labelledby="rename-dialog-title">
          <form novalidate @submit.prevent="submit">
            <h2 id="rename-dialog-title" class="dialog-title">改名</h2>
            <p v-if="node" class="dialog-sub">当前名称：{{ node.name }}</p>

            <div class="field">
              <label class="field-label" for="rename-name">新名称</label>
              <input
                id="rename-name"
                ref="inputRef"
                v-model="name"
                class="input"
                type="text"
                name="node-name"
                maxlength="255"
                autocomplete="off"
                spellcheck="false"
                :aria-invalid="fieldError ? 'true' : undefined"
                :aria-describedby="fieldError ? 'rename-error' : 'rename-hint'"
                @input="onInput"
              />
              <p v-if="fieldError" id="rename-error" class="field-error" role="alert">{{ fieldError }}</p>
              <p v-else id="rename-hint" class="field-hint">{{ FOLDER_NAME_HINT }}</p>
            </div>

            <p v-if="submitError" class="submit-error" role="alert">{{ submitError }}</p>
            <button v-if="stale" class="stale-refresh" type="button" @click="refreshList">刷新列表</button>

            <div class="dialog-actions">
              <button class="btn btn-secondary" type="button" :disabled="submitting" @click="requestClose">
                取消
              </button>
              <button class="btn btn-primary" type="submit" :disabled="!canSubmit">
                {{ submitting ? '保存中…' : '保存' }}
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
  width: min(400px, 100%);
  padding: 22px 22px 18px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.dialog-title {
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
}

.dialog-sub {
  margin-top: 4px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  overflow-wrap: anywhere;
}

.field {
  margin-top: 16px;
}

.submit-error {
  margin-top: 10px;
  font-size: var(--fs-sm);
  color: var(--color-danger);
}

.stale-refresh {
  margin-top: 6px;
  font-size: var(--fs-sm);
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
