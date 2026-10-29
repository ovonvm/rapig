<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BrandMark from '../components/BrandMark.vue'
import { ApiError, fetchRegistrationEnabled } from '../api/client'
import { messageForError } from '../lib/errors'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const username = ref('')
const password = ref('')
const error = ref<string | null>(null)
const submitting = ref(false)
const usernameInput = ref<HTMLInputElement | null>(null)
const lockedFor = ref(0)
const registrationEnabled = ref(false)
let lockTimer: number | null = null

const disabled = computed(() => submitting.value || lockedFor.value > 0)
const displayError = computed(() => {
  if (lockedFor.value > 0) return `尝试次数过多，请 ${lockedFor.value} 秒后再试`
  return error.value
})

function startLock(seconds: number): void {
  const total = Number.isFinite(seconds) && seconds > 0 ? Math.ceil(seconds) : 30
  lockedFor.value = total
  if (lockTimer !== null) window.clearInterval(lockTimer)
  lockTimer = window.setInterval(() => {
    lockedFor.value = Math.max(0, lockedFor.value - 1)
    if (lockedFor.value === 0 && lockTimer !== null) {
      window.clearInterval(lockTimer)
      lockTimer = null
    }
  }, 1000)
}

onMounted(() => {
  // The username field may be pre-filled by the registration flow.
  const preset = route.query.username
  if (typeof preset === 'string' && preset && !username.value) username.value = preset
  usernameInput.value?.focus()
  void fetchRegistrationEnabled()
    .then((enabled) => {
      registrationEnabled.value = enabled
    })
    .catch(() => {
      registrationEnabled.value = false
    })
})

onBeforeUnmount(() => {
  if (lockTimer !== null) window.clearInterval(lockTimer)
})

function loginErrorMessage(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.code === 'invalid_credentials' || cause.status === 401) {
      return cause.message || '用户名或密码错误'
    }
    // 403 `deletion_pending`, rate limits etc. get their contract copy here.
    return messageForError(cause, '登录失败，请稍后重试')
  }
  return '无法连接服务器，请稍后重试'
}

async function submit(): Promise<void> {
  if (disabled.value) return

  error.value = null

  if (!username.value.trim() || password.value.length === 0) {
    error.value = '请输入用户名/邮箱/手机号与密码'
    return
  }

  submitting.value = true

  try {
    await auth.login(username.value.trim(), password.value)

    const redirect = route.query.redirect
    const target =
      typeof redirect === 'string' && redirect.startsWith('/') && !redirect.startsWith('//')
        ? redirect
        : '/files'
    await router.replace(target)
  } catch (cause) {
    if (cause instanceof ApiError && cause.code === 'too_many_attempts') {
      startLock(cause.retryAfter ?? 30)
      error.value = null
    } else {
      error.value = loginErrorMessage(cause)
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-card" aria-labelledby="login-wordmark">
      <div class="login-brand">
        <BrandMark :size="30" />
        <h1 id="login-wordmark" class="login-wordmark">Rapig</h1>
      </div>
      <p class="login-subtitle">自托管网盘</p>

      <form class="login-form" novalidate @submit.prevent="submit">
        <div class="field">
          <label class="field-label" for="login-username">用户名 / 邮箱 / 手机号</label>
          <input
            id="login-username"
            ref="usernameInput"
            v-model="username"
            class="input"
            type="text"
            name="username"
            autocomplete="username"
            autocapitalize="off"
            autocorrect="off"
            spellcheck="false"
            :disabled="disabled"
            :aria-invalid="displayError ? 'true' : undefined"
          />
        </div>

        <div class="field">
          <label class="field-label" for="login-password">密码</label>
          <input
            id="login-password"
            v-model="password"
            class="input"
            type="password"
            name="password"
            autocomplete="current-password"
            :disabled="disabled"
            :aria-invalid="displayError ? 'true' : undefined"
          />
        </div>

        <p v-if="displayError" class="login-error" role="alert">
          <svg class="login-error-icon" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <circle cx="7" cy="7" r="5.6" fill="none" stroke="currentColor" stroke-width="1.3" opacity="0.5" />
            <path d="M7 4.2v3.6M7 10.2v.1" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" />
          </svg>
          <span>{{ displayError }}</span>
        </p>

        <button class="btn btn-primary btn-block login-submit" type="submit" :disabled="disabled">
          {{ submitting ? '登录中…' : '登录' }}
        </button>
      </form>

      <p v-if="registrationEnabled" class="login-footer">
        还没有账号？
        <RouterLink class="login-link" :to="{ name: 'register' }">注册账号</RouterLink>
      </p>
    </section>
  </main>
</template>

<style scoped>
.login-page {
  display: grid;
  min-height: 100%;
  padding: 24px;
  place-items: center;
}

.login-card {
  width: min(380px, 100%);
  padding: 28px 28px 26px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.login-brand {
  display: flex;
  align-items: center;
  gap: 11px;
}

.login-wordmark {
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1.1;
}

.login-subtitle {
  margin-top: 6px;
  padding-left: 41px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-top: 24px;
  padding-top: 24px;
  border-top: 1px solid var(--color-border);
}

.login-error {
  display: flex;
  align-items: flex-start;
  gap: 7px;
  padding: 9px 11px;
  background: var(--color-danger-soft);
  border-radius: var(--radius-sm);
  font-size: var(--fs-sm);
  color: var(--color-danger);
}

.login-error-icon {
  flex: none;
  margin-top: 2px;
}

.login-submit {
  height: 38px;
}

.login-footer {
  margin: 18px 0 0;
  padding-top: 14px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  text-align: center;
  border-top: 1px solid var(--color-border);
}

.login-link {
  color: var(--color-primary-ink);
  font-weight: 500;
}

.login-link:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}
</style>
