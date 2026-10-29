<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { copyText } from '../lib/clipboard'
import { messageForDeletion, messageForPasswordChange } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import { useAuthStore } from '../stores/auth'
import { useSettingsStore } from '../stores/settings'
import { useToast } from '../composables/useToast'

const auth = useAuthStore()
const settings = useSettingsStore()
const router = useRouter()
const toast = useToast()

const passwordForm = reactive({ current: '', next: '', confirm: '' })
const passwordError = ref('')
const passwordFieldError = ref('')
const changingPassword = ref(false)

async function submitPassword(): Promise<void> {
  if (changingPassword.value) return

  passwordFieldError.value = ''
  passwordError.value = ''

  const current = passwordForm.current
  const next = passwordForm.next

  if (current.length === 0) {
    passwordFieldError.value = '请输入当前密码'
    return
  }

  if (next.length < 8) {
    passwordFieldError.value = '新密码至少 8 个字符'
    return
  }

  if (next === current) {
    passwordFieldError.value = '新密码不能与当前密码相同'
    return
  }

  if (next !== passwordForm.confirm) {
    passwordFieldError.value = '两次输入的新密码不一致'
    return
  }

  changingPassword.value = true
  try {
    await settings.changePassword(current, next)
    passwordForm.current = ''
    passwordForm.next = ''
    passwordForm.confirm = ''
    toast.push('密码已修改，其它设备上的登录已退出')
  } catch (cause) {
    passwordError.value = messageForPasswordChange(cause)
  } finally {
    changingPassword.value = false
  }
}

/* ------------------------------------------------- account deletion (M5.2c) */

const username = computed(() => auth.user?.username ?? '')

const deleteOpen = ref(false)
const deleteBusy = ref(false)
const deleteError = ref('')
const cancelToken = ref('')
const purgeAt = ref('')
const linkCopied = ref(false)

const deleteForm = reactive({ password: '', confirmName: '' })
const passwordRef = ref<HTMLInputElement | null>(null)
const linkInputRef = ref<HTMLInputElement | null>(null)
let restoreFocus: HTMLElement | null = null

const cancelLink = computed(
  () => `${window.location.origin}/cancel-deletion?token=${encodeURIComponent(cancelToken.value)}`,
)
const purgeAtLabel = computed(() => (purgeAt.value ? formatDateTime(purgeAt.value) : ''))

function openDeleteDialog(): void {
  deleteForm.password = ''
  deleteForm.confirmName = ''
  deleteError.value = ''
  cancelToken.value = ''
  purgeAt.value = ''
  linkCopied.value = false
  deleteOpen.value = true
}

function requestClose(): void {
  if (deleteBusy.value) return
  // The cancel link is shown exactly once: never close the result view by accident.
  if (cancelToken.value) return
  deleteOpen.value = false
}

async function submitDeletion(): Promise<void> {
  if (deleteBusy.value) return
  deleteError.value = ''

  if (deleteForm.password.length === 0) {
    deleteError.value = '请输入当前密码'
    passwordRef.value?.focus()
    return
  }

  if (deleteForm.confirmName.trim() !== username.value) {
    deleteError.value = '输入的用户名与当前账号不一致'
    return
  }

  deleteBusy.value = true
  try {
    const ticket = await settings.requestAccountDeletion(deleteForm.password)
    cancelToken.value = ticket.cancel_token
    purgeAt.value = ticket.purge_at
    deleteForm.password = ''
    await nextTick()
    linkInputRef.value?.focus()
    linkInputRef.value?.select()
  } catch (cause) {
    deleteError.value = messageForDeletion(cause)
  } finally {
    deleteBusy.value = false
  }
}

async function copyCancelLink(): Promise<void> {
  const ok = await copyText(cancelLink.value)
  linkCopied.value = ok
  if (ok) toast.push('撤销链接已复制')
  else {
    linkInputRef.value?.focus()
    linkInputRef.value?.select()
  }
}

async function closeAndLogout(): Promise<void> {
  try {
    await auth.logout()
  } catch {
    /* the local session is cleared in the store's finally block */
  }
  deleteOpen.value = false
  await router.replace({ name: 'login' })
}

function onDialogKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Escape') return
  event.stopPropagation()
  requestClose()
}

watch(deleteOpen, async (open) => {
  if (!open) {
    document.body.style.overflow = ''
    restoreFocus?.focus()
    restoreFocus = null
    return
  }
  restoreFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  document.body.style.overflow = 'hidden'
  await nextTick()
  passwordRef.value?.focus()
})

onBeforeUnmount(() => {
  document.body.style.overflow = ''
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <h1 class="page-title">设置</h1>
      <p class="page-sub">账号安全</p>
    </header>

    <div class="stack">
      <section class="panel" aria-labelledby="password-title">
        <header class="panel-head">
          <h2 id="password-title" class="panel-title">修改密码</h2>
          <p class="panel-hint">修改后，其它设备上的登录会被强制退出。</p>
        </header>

        <form class="form" novalidate @submit.prevent="submitPassword">
          <div class="field">
            <label class="field-label" for="password-current">当前密码</label>
            <input
              id="password-current"
              v-model="passwordForm.current"
              class="input"
              type="password"
              autocomplete="current-password"
              :disabled="changingPassword"
            />
          </div>

          <div class="field">
            <label class="field-label" for="password-next">新密码</label>
            <input
              id="password-next"
              v-model="passwordForm.next"
              class="input"
              type="password"
              autocomplete="new-password"
              :disabled="changingPassword"
            />
            <p class="field-hint">至少 8 个字符。</p>
          </div>

          <div class="field">
            <label class="field-label" for="password-confirm">确认新密码</label>
            <input
              id="password-confirm"
              v-model="passwordForm.confirm"
              class="input"
              type="password"
              autocomplete="new-password"
              :disabled="changingPassword"
            />
          </div>

          <p v-if="passwordFieldError" class="field-error" role="alert">{{ passwordFieldError }}</p>
          <p v-if="passwordError" class="alert" role="alert">{{ passwordError }}</p>

          <div class="form-actions">
            <button class="btn btn-primary" type="submit" :disabled="changingPassword">
              {{ changingPassword ? '保存中…' : '保存修改' }}
            </button>
          </div>
        </form>
      </section>

      <section class="panel panel-danger" aria-labelledby="deletion-title">
        <header class="panel-head">
          <h2 id="deletion-title" class="panel-title danger-title">注销账号</h2>
          <p class="panel-hint">
            提交后账号立即不可用，3 天后永久删除；删除后用户名保留 3 个月。此操作不可撤销，除非在宽限期内使用撤销链接或联系管理员恢复。
          </p>
        </header>

        <div class="danger-actions">
          <button type="button" class="btn btn-danger" @click="openDeleteDialog">申请注销</button>
        </div>
      </section>
    </div>

    <Teleport to="body">
      <Transition name="dialog">
        <div v-if="deleteOpen" class="overlay" @mousedown.self="requestClose">
          <div
            class="dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="deletion-dialog-title"
            @keydown="onDialogKeydown"
          >
            <template v-if="!cancelToken">
              <h2 id="deletion-dialog-title" class="dialog-title">申请注销账号</h2>
              <p class="dialog-sub">
                账号会立即无法登录，3 天后连同所有文件被永久删除。删除后用户名保留 3 个月。
              </p>

              <form class="dialog-form" novalidate @submit.prevent="submitDeletion">
                <label class="field">
                  <span class="field-label">当前密码</span>
                  <input
                    id="deletion-password"
                    ref="passwordRef"
                    v-model="deleteForm.password"
                    class="input"
                    type="password"
                    autocomplete="current-password"
                    :disabled="deleteBusy"
                  />
                </label>

                <label class="field">
                  <span class="field-label">确认用户名</span>
                  <input
                    id="deletion-username"
                    v-model="deleteForm.confirmName"
                    class="input"
                    type="text"
                    autocomplete="off"
                    :placeholder="`输入「${username}」以确认`"
                    :disabled="deleteBusy"
                  />
                </label>

                <p v-if="deleteError" class="field-error" role="alert">{{ deleteError }}</p>

                <div class="dialog-actions">
                  <button type="button" class="btn btn-secondary" :disabled="deleteBusy" @click="requestClose">
                    取消
                  </button>
                  <button type="submit" class="btn btn-danger" :disabled="deleteBusy">
                    {{ deleteBusy ? '提交中…' : '确认申请注销' }}
                  </button>
                </div>
              </form>
            </template>

            <template v-else>
              <h2 id="deletion-dialog-title" class="dialog-title">保存撤销链接</h2>
              <p class="dialog-sub">
                账号将在 <strong>{{ purgeAtLabel }}</strong> 被永久删除。在此期间可以用下面的链接撤销注销。
              </p>

              <div class="link-row">
                <input
                  ref="linkInputRef"
                  class="input link-input"
                  :value="cancelLink"
                  type="text"
                  readonly
                  aria-label="撤销链接"
                  @focus="($event.target as HTMLInputElement).select()"
                />
                <button type="button" class="btn btn-secondary" @click="copyCancelLink">
                  {{ linkCopied ? '已复制' : '复制' }}
                </button>
              </div>

              <p class="onetime-note" role="alert">该链接仅显示一次，请立即保存。关闭后无法再次查看。</p>

              <div class="dialog-actions">
                <button type="button" class="btn btn-primary" @click="closeAndLogout">关闭并退出登录</button>
              </div>
            </template>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.page {
  max-width: var(--page-max);
  margin: 0 auto;
}

.page-head {
  margin-bottom: 18px;
}

.page-title {
  font-size: var(--fs-xl);
  font-weight: 600;
  letter-spacing: -0.015em;
}

.page-sub {
  margin-top: 2px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.panel {
  padding: 18px 20px 20px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.panel-danger {
  border-color: color-mix(in srgb, var(--color-danger) 32%, var(--color-border));
}

.panel-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--color-border);
}

.panel-title {
  font-size: var(--fs-lg);
  font-weight: 600;
}

.danger-title {
  color: var(--color-danger);
}

.panel-hint {
  max-width: 64ch;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-width: 380px;
  padding-top: 16px;
}

.form-actions {
  display: flex;
  gap: 10px;
  padding-top: 2px;
}

.danger-actions {
  display: flex;
  gap: 10px;
  padding-top: 14px;
}

.alert {
  padding: 8px 11px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-soft);
  color: var(--color-danger);
  font-size: var(--fs-sm);
}

.btn-danger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 34px;
  padding: 0 13px;
  border-radius: var(--radius-sm);
  background: var(--color-danger);
  color: #fff;
  font-size: var(--fs-sm);
  font-weight: 500;
  transition: background-color var(--dur) var(--ease);
}

.btn-danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-danger) 86%, #000);
}

.btn-danger:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

/* dialog ------------------------------------------------------------- */

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
  overflow: auto;
  padding: 22px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.dialog-title {
  font-size: var(--fs-lg);
  font-weight: 600;
}

.dialog-sub {
  margin-top: 6px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  line-height: 1.6;
}

.dialog-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-top: 16px;
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

.link-row {
  display: flex;
  gap: 8px;
  margin-top: 14px;
}

.link-input {
  flex: 1;
  min-width: 0;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: var(--fs-xs);
}

.onetime-note {
  margin-top: 10px;
  padding: 9px 11px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-soft);
  color: var(--color-danger);
  font-size: var(--fs-sm);
}

.dialog-enter-active,
.dialog-leave-active {
  transition: opacity var(--dur) var(--ease);
}

.dialog-enter-from,
.dialog-leave-to {
  opacity: 0;
}

@media (max-width: 760px) {
  .panel {
    padding: 16px;
  }

  .form {
    max-width: none;
  }

  .dialog {
    padding: 18px;
  }

  .link-row {
    flex-direction: column;
  }
}
</style>
