<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import BrandMark from '../components/BrandMark.vue'
import { messageForCancelDeletion } from '../lib/errors'
import { useSettingsStore } from '../stores/settings'

const route = useRoute()
const settings = useSettingsStore()

type Phase = 'checking' | 'done' | 'failed'

const phase = ref<Phase>('checking')
const failureCopy = ref('链接无效或已过期')

const token = computed(() => {
  const raw = route.query.token
  return typeof raw === 'string' ? raw : ''
})

const title = computed(() => {
  if (phase.value === 'checking') return '正在恢复账号…'
  return phase.value === 'done' ? '账号已恢复' : '链接无效或已过期'
})

const description = computed(() => {
  if (phase.value === 'checking') return '正在校验撤销链接，请稍候。'
  if (phase.value === 'done') {
    return '注销申请已撤销，账号可以正常登录了。你的文件和分享都还在。'
  }
  return `${failureCopy.value}。撤销链接只能使用一次；超过宽限期后账号会被永久删除。`
})

onMounted(async () => {
  if (!token.value) {
    phase.value = 'failed'
    return
  }
  try {
    await settings.cancelAccountDeletion(token.value)
    phase.value = 'done'
  } catch (cause) {
    failureCopy.value = messageForCancelDeletion(cause)
    phase.value = 'failed'
  }
})
</script>

<template>
  <div class="public-page">
    <main class="public-shell">
      <header class="brand-row">
        <BrandMark :size="26" />
        <span class="wordmark">Rapig</span>
        <span class="brand-hint">账号注销</span>
      </header>

      <section class="card" aria-live="polite">
        <div class="icon" :class="phase" aria-hidden="true">
          <svg v-if="phase === 'done'" width="26" height="26" viewBox="0 0 24 24" fill="none">
            <path d="m5 12.5 4.2 4.2L19 7" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <svg v-else-if="phase === 'failed'" width="26" height="26" viewBox="0 0 24 24" fill="none">
            <circle cx="12" cy="12" r="8.2" stroke="currentColor" stroke-width="1.6" />
            <path d="M12 8v4.6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
            <circle cx="12" cy="15.7" r="0.9" fill="currentColor" />
          </svg>
          <span v-else class="spinner"></span>
        </div>

        <h1 class="title">{{ title }}</h1>
        <p class="description">{{ description }}</p>

        <RouterLink v-if="phase !== 'checking'" class="btn btn-primary" :to="{ name: 'login' }">
          去登录
        </RouterLink>
      </section>

      <p class="foot">由 Rapig 自托管网盘提供</p>
    </main>
  </div>
</template>

<style scoped>
.public-page {
  display: flex;
  min-height: 100vh;
  align-items: center;
  justify-content: center;
  padding: 24px 16px 40px;
  background: var(--color-bg);
}

.public-shell {
  display: flex;
  width: min(440px, 100%);
  flex-direction: column;
  gap: 14px;
}

.brand-row {
  display: flex;
  align-items: center;
  gap: 9px;
  padding-left: 2px;
}

.wordmark {
  font-size: var(--fs-lg);
  font-weight: 600;
  letter-spacing: -0.015em;
}

.brand-hint {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.card {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 34px 28px 30px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  text-align: center;
}

.icon {
  display: flex;
  width: 54px;
  height: 54px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.icon.done {
  background: var(--color-success-soft);
  color: var(--color-success);
}

.icon.failed {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.spinner {
  width: 20px;
  height: 20px;
  border: 2px solid var(--color-border-strong);
  border-top-color: var(--color-primary);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.title {
  margin-top: 16px;
  font-size: var(--fs-lg);
  font-weight: 600;
}

.description {
  margin-top: 6px;
  max-width: 34ch;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  line-height: 1.7;
}

.btn {
  margin-top: 20px;
}

.foot {
  text-align: center;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

@media (prefers-reduced-motion: reduce) {
  .spinner {
    animation: none;
  }
}
</style>
