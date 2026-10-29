<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, listStoragePolicies } from '../api/client'
import { defaultStoragePolicyId } from '../lib/files'
import type { StoragePolicyOption } from '../lib/files'
import { useFilesStore } from '../stores/files'
import { FOLDER_NAME_HINT, validateFolderName } from '../lib/validation'

const props = withDefaults(
  defineProps<{
    open: boolean
    atRoot?: boolean
    /** Optional create implementation for non-personal scopes (P1 group space). */
    create?: (name: string) => Promise<unknown>
  }>(),
  { atRoot: false },
)
const emit = defineEmits<{ close: []; created: [name: string] }>()

const files = useFilesStore()

const dialogRef = ref<HTMLElement | null>(null)
const inputRef = ref<HTMLInputElement | null>(null)
const name = ref('')
const fieldError = ref<string | null>(null)
const submitError = ref<string | null>(null)
const submitting = ref(false)

/** M5.3: at the drive root more than one storage root may be selectable. */
const policies = ref<StoragePolicyOption[]>([])
const policiesLoading = ref(false)
const policyId = ref<number | null>(null)
const showStorage = computed(() => props.atRoot && !policiesLoading.value && policies.value.length > 1)

const canSubmit = computed(() => !submitting.value)

async function loadPolicies(): Promise<void> {
  policiesLoading.value = true
  try {
    const list = (await listStoragePolicies()).filter((policy) => policy.enabled)
    policies.value = list
    policyId.value = defaultStoragePolicyId(list)
  } catch {
    // Storage selection is an enhancement; the default root still works.
    policies.value = []
    policyId.value = null
  } finally {
    policiesLoading.value = false
  }
}

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      document.body.style.overflow = ''
      return
    }

    name.value = ''
    fieldError.value = null
    submitError.value = null
    submitting.value = false
    policies.value = []
    policiesLoading.value = false
    policyId.value = null
    document.body.style.overflow = 'hidden'
    await nextTick()
    inputRef.value?.focus()
    if (props.atRoot) void loadPolicies()
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
}

function folderErrorMessage(cause: unknown): string {
  if (!(cause instanceof ApiError)) return '新建文件夹失败，请稍后重试'

  switch (cause.code) {
    case 'name_conflict':
      return '同名文件夹已存在'
    case 'parent_not_found':
      return '目标位置不存在，请刷新后重试'
    case 'invalid_target':
      // 400 invalid_target: the chosen storage policy is gone or disabled.
      return cause.message || '所选存储位置不可用，请刷新后重试'
    case 'csrf_failed':
    case 'unauthenticated':
      return '登录状态已失效，请重新登录'
    default:
      return cause.message
  }
}

async function submit(): Promise<void> {
  if (submitting.value) return

  const validation = validateFolderName(name.value)
  if (validation) {
    fieldError.value = validation
    inputRef.value?.focus()
    return
  }

  const folderName = name.value.trim()
  fieldError.value = null
  submitError.value = null
  submitting.value = true

  try {
    if (props.create) await props.create(folderName)
    else await files.createFolder(folderName, showStorage.value ? policyId.value : null)
    emit('created', folderName)
  } catch (cause) {
    submitError.value = folderErrorMessage(cause)
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
    root.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled])'),
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
        <div
          ref="dialogRef"
          class="dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="folder-dialog-title"
        >
          <form novalidate @submit.prevent="submit">
            <h2 id="folder-dialog-title" class="dialog-title">新建文件夹</h2>

            <div class="field">
              <label class="field-label" for="new-folder-name">名称</label>
              <input
                id="new-folder-name"
                ref="inputRef"
                v-model="name"
                class="input"
                type="text"
                name="folder-name"
                maxlength="255"
                autocomplete="off"
                spellcheck="false"
                placeholder="例如：工作资料"
                :aria-invalid="fieldError ? 'true' : undefined"
                :aria-describedby="fieldError ? 'new-folder-error' : 'new-folder-hint'"
                @input="onInput"
              />
              <p v-if="fieldError" id="new-folder-error" class="field-error" role="alert">{{ fieldError }}</p>
              <p v-else id="new-folder-hint" class="field-hint">{{ FOLDER_NAME_HINT }}</p>
            </div>

            <div v-if="showStorage" class="field">
              <label class="field-label" for="new-folder-storage">存储位置</label>
              <select id="new-folder-storage" v-model.number="policyId" class="input" :disabled="submitting">
                <option v-for="policy in policies" :key="policy.id" :value="policy.id">
                  {{ policy.name }}{{ policy.is_default ? '（默认）' : '' }}
                </option>
              </select>
              <p class="field-hint">根目录可以选择存储位置，子文件夹会沿用上级的存储。</p>
            </div>

            <p v-if="submitError" class="submit-error" role="alert">{{ submitError }}</p>

            <div class="dialog-actions">
              <button class="btn btn-secondary" type="button" :disabled="submitting" @click="requestClose">
                取消
              </button>
              <button class="btn btn-primary" type="submit" :disabled="!canSubmit">
                {{ submitting ? '创建中…' : '创建' }}
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
  margin-bottom: 14px;
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
}

.submit-error {
  margin-top: 10px;
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
