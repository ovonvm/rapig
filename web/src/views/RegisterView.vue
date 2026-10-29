<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import BrandMark from '../components/BrandMark.vue'
import { ApiError, fetchRegistrationEnabled, registerAccount } from '../api/client'
import { useToast } from '../composables/useToast'
import { messageForRegister } from '../lib/errors'
import {
  EMAIL_MAX,
  identifierPayload,
  normalizeEmail,
  normalizePhone,
  validateEmailField,
  validatePhoneField,
  validateUsernameField,
} from '../lib/account'

const router = useRouter()
const toast = useToast()

const PASSWORD_MIN = 8
const PASSWORD_MAX = 128

type Phase = 'checking' | 'form' | 'disabled' | 'unreachable'

const phase = ref<Phase>('checking')
const checkError = ref<string | null>(null)

const form = reactive({
  username: '',
  email: '',
  phone: '',
  password: '',
  confirm: '',
})
const showPassword = ref(false)

const attempted = ref(false)
const submitting = ref(false)
const submitError = ref<string | null>(null)
const usernameInput = ref<HTMLInputElement | null>(null)

const touched = reactive({ username: false, email: false, phone: false, password: false, confirm: false })

const fieldErrors = computed(() => ({
  username: validateUsernameField(form.username),
  email: validateEmailField(form.email),
  phone: validatePhoneField(form.phone),
  password: validatePassword(form.password),
  confirm: validateConfirm(form.confirm, form.password),
}))

const identifierMissing = computed(() => {
  return !form.username.trim() && !form.email.trim() && !form.phone.trim()
})

const hasAnyError = computed(() => {
  const errors = fieldErrors.value
  return Boolean(errors.username || errors.email || errors.phone || errors.password || errors.confirm || identifierMissing.value)
})

const disabled = computed(() => submitting.value)

function validatePassword(value: string): string | null {
  if (!value) return '请输入密码'
  if (value.length < PASSWORD_MIN) return `密码至少需要 ${PASSWORD_MIN} 个字符`
  if (value.length > PASSWORD_MAX) return `密码不能超过 ${PASSWORD_MAX} 个字符`
  return null
}

function validateConfirm(value: string, password: string): string | null {
  if (!value) return '请再次输入密码'
  if (value !== password) return '两次输入的密码不一致'
  return null
}

function showsError(field: keyof typeof touched): boolean {
  if (attempted.value) return true
  if (field === 'confirm' && attempted.value) return true
  return touched[field]
}

function setEnabled(enabled: boolean): void {
  phase.value = enabled ? 'form' : 'disabled'
  if (enabled) {
    void nextTick(() => usernameInput.value?.focus())
  }
}

async function checkRegistration(): Promise<void> {
  phase.value = 'checking'
  checkError.value = null
  try {
    setEnabled(await fetchRegistrationEnabled())
  } catch (cause) {
    checkError.value = cause instanceof ApiError && cause.code === 'network_error'
      ? '无法连接服务器，请检查网络后重试'
      : '无法确认注册状态，请稍后重试'
    phase.value = 'unreachable'
  }
}

async function submit(): Promise<void> {
  if (disabled.value) return
  attempted.value = true
  submitError.value = null

  if (identifierMissing.value) {
    submitError.value = '请至少填写用户名、邮箱或手机号其中一项'
    return
  }

  if (hasAnyError.value) return

  submitting.value = true
  try {
    const payload = identifierPayload({
      username: form.username,
      email: normalizeEmail(form.email),
      phone: normalizePhone(form.phone),
    })
    await registerAccount({ ...payload, password: form.password })
    toast.push('注册成功，请登录')
    const query = payload.username ? { username: payload.username } : {}
    await router.replace({ name: 'login', query })
  } catch (cause) {
    if (cause instanceof ApiError && (cause.reason === 'registration_disabled' || cause.code === 'registration_disabled')) {
      setEnabled(false)
      return
    }
    submitError.value = messageForRegister(cause)
  } finally {
    submitting.value = false
  }
}

function onFieldBlur(field: keyof typeof touched): void {
  touched[field] = true
}

onMounted(() => {
  void checkRegistration()
})
</script>

<template>
  <main class="register-page">
    <section class="register-card" aria-labelledby="register-title">
      <div class="register-brand">
        <BrandMark :size="30" />
        <h1 id="register-title" class="register-wordmark">Rapig</h1>
      </div>
      <p class="register-subtitle">创建账号 · 自托管网盘</p>

      <section v-if="phase === 'checking'" class="register-note" role="status">
        <span class="spinner" aria-hidden="true"></span>
        <span>正在检查注册状态…</span>
      </section>

      <section v-else-if="phase === 'disabled'" class="register-note is-blocked" role="alert">
        <p class="note-title">站点未开放注册</p>
        <p class="note-copy">管理员暂未开放自助注册。如需账号，请联系站点管理员。</p>
        <RouterLink class="btn btn-primary btn-block" :to="{ name: 'login' }">返回登录</RouterLink>
      </section>

      <section v-else-if="phase === 'unreachable'" class="register-note is-blocked" role="alert">
        <p class="note-title">暂时无法注册</p>
        <p class="note-copy">{{ checkError }}</p>
        <div class="note-actions">
          <button type="button" class="btn btn-secondary" @click="checkRegistration">重试</button>
          <RouterLink class="btn btn-secondary" :to="{ name: 'login' }">返回登录</RouterLink>
        </div>
      </section>

      <form v-else class="register-form" novalidate @submit.prevent="submit">
        <p class="form-tip">用户名、邮箱、手机号填一项即可，任一都可登录。</p>

        <div class="field">
          <label class="field-label" for="register-username">用户名（选填）</label>
          <input
            id="register-username"
            ref="usernameInput"
            v-model="form.username"
            class="input"
            type="text"
            name="username"
            autocomplete="username"
            autocapitalize="off"
            autocorrect="off"
            spellcheck="false"
            maxlength="32"
            placeholder="2–32 个字符"
            :disabled="disabled"
            :aria-invalid="showsError('username') && Boolean(fieldErrors.username) ? 'true' : undefined"
            :aria-describedby="showsError('username') && fieldErrors.username ? 'register-username-error' : undefined"
            @blur="onFieldBlur('username')"
          />
          <p v-if="showsError('username') && fieldErrors.username" id="register-username-error" class="field-error">
            {{ fieldErrors.username }}
          </p>
        </div>

        <div class="field">
          <label class="field-label" for="register-email">邮箱（选填）</label>
          <input
            id="register-email"
            v-model="form.email"
            class="input"
            type="email"
            name="email"
            autocomplete="email"
            autocapitalize="off"
            autocorrect="off"
            spellcheck="false"
            :maxlength="EMAIL_MAX"
            placeholder="you@example.com"
            :disabled="disabled"
            :aria-invalid="showsError('email') && Boolean(fieldErrors.email) ? 'true' : undefined"
            @blur="onFieldBlur('email')"
          />
          <p v-if="showsError('email') && fieldErrors.email" class="field-error">{{ fieldErrors.email }}</p>
        </div>

        <div class="field">
          <label class="field-label" for="register-phone">手机号（选填）</label>
          <input
            id="register-phone"
            v-model="form.phone"
            class="input"
            type="tel"
            name="phone"
            autocomplete="tel"
            inputmode="tel"
            placeholder="可带 + 前缀"
            :disabled="disabled"
            :aria-invalid="showsError('phone') && Boolean(fieldErrors.phone) ? 'true' : undefined"
            @blur="onFieldBlur('phone')"
          />
          <p v-if="showsError('phone') && fieldErrors.phone" class="field-error">{{ fieldErrors.phone }}</p>
        </div>

        <div class="field">
          <label class="field-label" for="register-password">密码</label>
          <div class="password-row">
            <input
              id="register-password"
              v-model="form.password"
              class="input"
              :type="showPassword ? 'text' : 'password'"
              name="new-password"
              autocomplete="new-password"
              :disabled="disabled"
              :aria-invalid="showsError('password') && Boolean(fieldErrors.password) ? 'true' : undefined"
              @blur="onFieldBlur('password')"
            />
            <button
              type="button"
              class="password-toggle"
              :aria-label="showPassword ? '隐藏密码' : '显示密码'"
              :aria-pressed="showPassword"
              @click="showPassword = !showPassword"
            >
              <svg v-if="showPassword" width="16" height="16" viewBox="0 0 20 20" aria-hidden="true">
                <path d="M2.2 10S4.8 5.4 10 5.4 17.8 10 17.8 10 15.2 14.6 10 14.6 2.2 10 2.2 10Z" fill="none" stroke="currentColor" stroke-width="1.4" />
                <circle cx="10" cy="10" r="2.3" fill="none" stroke="currentColor" stroke-width="1.4" />
              </svg>
              <svg v-else width="16" height="16" viewBox="0 0 20 20" aria-hidden="true">
                <path d="M2.2 10S4.8 5.4 10 5.4 17.8 10 17.8 10 15.2 14.6 10 14.6 2.2 10 2.2 10Z" fill="none" stroke="currentColor" stroke-width="1.4" />
                <circle cx="10" cy="10" r="2.3" fill="none" stroke="currentColor" stroke-width="1.4" />
                <path d="m3.4 3.4 13.2 13.2" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
              </svg>
            </button>
          </div>
          <p v-if="showsError('password') && fieldErrors.password" class="field-error">{{ fieldErrors.password }}</p>
          <p v-else class="field-hint">至少 {{ PASSWORD_MIN }} 个字符。</p>
        </div>

        <div class="field">
          <label class="field-label" for="register-confirm">确认密码</label>
          <input
            id="register-confirm"
            v-model="form.confirm"
            class="input"
            :type="showPassword ? 'text' : 'password'"
            name="confirm-password"
            autocomplete="new-password"
            :disabled="disabled"
            :aria-invalid="showsError('confirm') && Boolean(fieldErrors.confirm) ? 'true' : undefined"
            @blur="onFieldBlur('confirm')"
          />
          <p v-if="showsError('confirm') && fieldErrors.confirm" class="field-error">{{ fieldErrors.confirm }}</p>
        </div>

        <p v-if="submitError" class="register-error" role="alert">{{ submitError }}</p>
        <p v-else-if="attempted && identifierMissing" class="register-error" role="alert">
          请至少填写用户名、邮箱或手机号其中一项
        </p>

        <button class="btn btn-primary btn-block register-submit" type="submit" :disabled="disabled">
          {{ submitting ? '注册中…' : '注册' }}
        </button>
      </form>

      <p class="register-footer">
        已有账号？
        <RouterLink class="register-link" :to="{ name: 'login' }">返回登录</RouterLink>
      </p>
    </section>
  </main>
</template>

<style scoped>
.register-page {
  display: grid;
  min-height: 100%;
  padding: 24px;
  place-items: center;
}

.register-card {
  width: min(400px, 100%);
  padding: 28px 28px 24px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.register-brand {
  display: flex;
  align-items: center;
  gap: 11px;
}

.register-wordmark {
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1.1;
}

.register-subtitle {
  margin-top: 6px;
  padding-left: 41px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.register-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-top: 22px;
  padding-top: 20px;
  border-top: 1px solid var(--color-border);
}

.form-tip {
  margin: 0;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.register-note {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 22px;
  padding-top: 20px;
  border-top: 1px solid var(--color-border);
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.register-note.is-blocked {
  align-items: stretch;
}

.note-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
  color: var(--color-text);
}

.note-copy {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.note-actions {
  display: flex;
  gap: 8px;
}

.spinner {
  width: 14px;
  height: 14px;
  border: 2px solid var(--color-border-strong);
  border-top-color: var(--color-primary);
  border-radius: 50%;
  animation: spin 700ms linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.password-row {
  position: relative;
}

.password-row .input {
  padding-right: 38px;
}

.password-toggle {
  position: absolute;
  top: 50%;
  right: 6px;
  display: grid;
  width: 28px;
  height: 28px;
  color: var(--color-muted);
  border-radius: var(--radius-sm);
  transform: translateY(-50%);
  place-items: center;
}

.password-toggle:hover {
  color: var(--color-text);
  background: var(--color-bg);
}

.register-error {
  margin: 0;
  padding: 9px 11px;
  font-size: var(--fs-sm);
  color: var(--color-danger);
  background: var(--color-danger-soft);
  border-radius: var(--radius-sm);
}

.register-submit {
  height: 38px;
  margin-top: 2px;
}

.register-footer {
  margin: 18px 0 0;
  padding-top: 14px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  text-align: center;
  border-top: 1px solid var(--color-border);
}

.register-link {
  color: var(--color-primary-ink);
  font-weight: 500;
}

.register-link:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}

@media (max-width: 420px) {
  .register-card {
    padding: 22px 20px 20px;
  }

  .register-subtitle {
    padding-left: 0;
  }
}
</style>
