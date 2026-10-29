<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    message: string
    detail?: string
    confirmLabel?: string
    danger?: boolean
    busy?: boolean
    error?: string | null
    /** When set, the user must type this exact text before confirming. */
    requireText?: string
  }>(),
  { detail: '', confirmLabel: '确定', danger: false, busy: false, error: null, requireText: '' },
)

const emit = defineEmits<{ close: []; confirm: [] }>()

const dialogRef = ref<HTMLElement | null>(null)
const confirmRef = ref<HTMLButtonElement | null>(null)
const typeInputRef = ref<HTMLInputElement | null>(null)
const typedText = ref('')

const typedOk = computed(() => props.requireText === '' || typedText.value.trim() === props.requireText)
const confirmDisabled = computed(() => props.busy || !typedOk.value)

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      document.body.style.overflow = ''
      typedText.value = ''
      return
    }
    typedText.value = ''
    document.body.style.overflow = 'hidden'
    await nextTick()
    if (props.requireText) typeInputRef.value?.focus()
    else confirmRef.value?.focus()
  },
)

onBeforeUnmount(() => {
  document.body.style.overflow = ''
})

function requestClose(): void {
  if (props.busy) return
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

  const focusable = Array.from(root.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])'))
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
        <div ref="dialogRef" class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title">
          <h2 id="confirm-title" class="dialog-title">{{ title }}</h2>
          <p class="dialog-message">{{ message }}</p>
          <p v-if="detail" class="dialog-detail">{{ detail }}</p>

          <label v-if="requireText" class="dialog-typed">
            <span class="dialog-typed-label">请输入「{{ requireText }}」以确认</span>
            <input
              ref="typeInputRef"
              v-model="typedText"
              class="input"
              type="text"
              :placeholder="requireText"
              autocomplete="off"
              spellcheck="false"
            />
          </label>

          <p v-if="error" class="dialog-error" role="alert">{{ error }}</p>

          <div class="dialog-actions">
            <button class="btn btn-secondary" type="button" :disabled="busy" @click="requestClose">取消</button>
            <button
              ref="confirmRef"
              class="btn"
              :class="danger ? 'btn-danger' : 'btn-primary'"
              type="button"
              :disabled="confirmDisabled"
              @click="emit('confirm')"
            >
              {{ busy ? '处理中…' : confirmLabel }}
            </button>
          </div>
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

.dialog-message {
  margin-top: 10px;
  font-size: var(--fs-base);
  color: var(--color-text);
  overflow-wrap: anywhere;
}

.dialog-detail {
  margin-top: 6px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.dialog-error {
  margin-top: 10px;
  font-size: var(--fs-sm);
  color: var(--color-danger);
}

.dialog-typed {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 14px;
}

.dialog-typed-label {
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 18px;
}

.btn-danger {
  background: var(--color-danger);
  color: #ffffff;
}

.btn-danger:hover:not(:disabled) {
  background: var(--color-danger);
  filter: brightness(0.92);
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
