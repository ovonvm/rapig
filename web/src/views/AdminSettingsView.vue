<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useToast } from '../composables/useToast'
import { formatBytes } from '../lib/format'
import { adminErrorMessage, bytesToGbText, parseGbInput } from '../lib/admin'
import { useAdminStore } from '../stores/admin'
import { useAuthStore } from '../stores/auth'

const admin = useAdminStore()
const auth = useAuthStore()
const toast = useToast()

const SITE_NAME_MAX = 64

/** Only the root administrator may flip the registration/deletion switches. */
const canEditRootSettings = computed(() => auth.isRoot)

const form = reactive({
  siteName: '',
  registrationEnabled: false,
  deletionEnabled: false,
  quotaGb: '0',
})

const touched = ref(false)
const formError = ref<string | null>(null)
const saveError = ref<string | null>(null)

const quotaBytes = computed(() => parseGbInput(form.quotaGb))

const original = computed(() => {
  const settings = admin.settings
  if (!settings) return null
  return {
    siteName: settings.site_name,
    registrationEnabled: settings.registration_enabled,
    deletionEnabled: settings.deletion_enabled,
    quotaGb: bytesToGbText(settings.default_quota_bytes),
  }
})

const dirty = computed(() => {
  const base = original.value
  if (!base) return false
  return (
    form.siteName !== base.siteName ||
    form.registrationEnabled !== base.registrationEnabled ||
    form.deletionEnabled !== base.deletionEnabled ||
    form.quotaGb !== base.quotaGb
  )
})

function fillFromStore(): void {
  const base = original.value
  if (!base) return
  form.siteName = base.siteName
  form.registrationEnabled = base.registrationEnabled
  form.deletionEnabled = base.deletionEnabled
  form.quotaGb = base.quotaGb
  touched.value = false
  formError.value = null
  saveError.value = null
}

watch(() => admin.settings, fillFromStore)

function validate(): string | null {
  const name = form.siteName.trim()
  if (!name) return '站点名不能为空'
  if (Array.from(name).length > SITE_NAME_MAX) return `站点名不能超过 ${SITE_NAME_MAX} 个字符`
  if (quotaBytes.value === null) return '默认配额请填写不小于 0 的数字（GB，0 表示不限）'
  return null
}

async function save(): Promise<void> {
  touched.value = true
  const problem = validate()
  formError.value = problem
  if (problem) return
  saveError.value = null
  try {
    const patch = {
      site_name: form.siteName.trim(),
      default_quota_bytes: quotaBytes.value ?? 0,
      // Non-root admins must not send either root-only key: the server answers 403.
      ...(canEditRootSettings.value
        ? {
            registration_enabled: form.registrationEnabled,
            deletion_enabled: form.deletionEnabled,
          }
        : {}),
    }
    const ok = await admin.saveSettings(patch)
    if (ok) {
      fillFromStore()
      toast.push('系统设置已保存')
    }
  } catch (cause) {
    saveError.value = adminErrorMessage(cause, '保存失败，请稍后重试')
  }
}

async function retryLoad(): Promise<void> {
  await admin.loadSettings()
}

onMounted(() => {
  void admin.loadSettings()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">系统设置</h1>
        <p class="page-sub">这些设置对整个实例生效，保存后立即应用于新会话。</p>
      </div>
    </header>

    <section v-if="admin.settingsLoading && !admin.settings" class="panel state-panel">
      <span class="skeleton-block wide"></span>
      <span class="skeleton-block"></span>
      <span class="skeleton-block short"></span>
    </section>

    <section v-else-if="admin.settingsError && !admin.settings" class="panel state-panel">
      <p class="state-title" role="alert">加载失败</p>
      <p class="state-copy">{{ admin.settingsError }}</p>
      <button type="button" class="btn btn-secondary" @click="retryLoad">重试</button>
    </section>

    <form v-else-if="admin.settings" class="panel form" novalidate @submit.prevent="save">
      <div class="form-block">
        <label class="field">
          <span class="field-label">站点名称</span>
          <input
            v-model="form.siteName"
            class="input"
            type="text"
            maxlength="64"
            placeholder="Rapig"
            :aria-invalid="touched && Boolean(formError && !form.siteName.trim())"
          />
          <span class="field-hint">显示在分享页与浏览器标题中，最多 {{ SITE_NAME_MAX }} 个字符。</span>
        </label>

        <div class="switch-row">
          <div class="switch-copy">
            <span class="field-label">允许注册</span>
            <span class="field-hint">注册默认关闭；仅 root 管理员可修改。</span>
            <span v-if="!canEditRootSettings" class="field-hint">当前账号不是 root 管理员，开关不可修改。</span>
          </div>
          <label class="switch" :class="{ 'is-disabled': !canEditRootSettings }">
            <input
              v-model="form.registrationEnabled"
              type="checkbox"
              role="switch"
              aria-label="允许注册"
              :disabled="!canEditRootSettings"
              :title="canEditRootSettings ? '' : '仅 root 管理员可修改'"
            />
            <span class="track" aria-hidden="true"><span class="thumb"></span></span>
          </label>
        </div>

        <div class="switch-row">
          <div class="switch-copy">
            <span class="field-label">允许用户注销账号</span>
            <span class="field-hint">关闭后用户无法自助注销；注销有 3 天宽限期，期间可联系管理员恢复。仅 root 管理员可修改。</span>
            <span v-if="!canEditRootSettings" class="field-hint">当前账号不是 root 管理员，开关不可修改。</span>
          </div>
          <label class="switch" :class="{ 'is-disabled': !canEditRootSettings }">
            <input
              v-model="form.deletionEnabled"
              type="checkbox"
              role="switch"
              aria-label="允许用户注销账号"
              :disabled="!canEditRootSettings"
              :title="canEditRootSettings ? '' : '仅 root 管理员可修改'"
            />
            <span class="track" aria-hidden="true"><span class="thumb"></span></span>
          </label>
        </div>

        <label class="field narrow">
          <span class="field-label">新用户默认配额（GB）</span>
          <input v-model="form.quotaGb" class="input" type="text" inputmode="decimal" placeholder="0" />
          <span class="field-hint">十进制 GB；0 表示不限。当前 {{ formatBytes(quotaBytes ?? 0) }}。</span>
        </label>
      </div>

      <div class="readonly">
        <p class="readonly-title">保留策略（只读）</p>
        <dl class="readonly-grid">
          <div>
            <dt>审计日志保留</dt>
            <dd class="numeric">{{ admin.settings.audit_retention_days }} 天</dd>
          </div>
          <div>
            <dt>回收站保留</dt>
            <dd class="numeric">{{ admin.settings.trash_retention_days }} 天</dd>
          </div>
        </dl>
      </div>

      <p v-if="formError" class="field-error" role="alert">{{ formError }}</p>
      <p v-if="saveError" class="field-error" role="alert">{{ saveError }}</p>

      <div class="form-actions">
        <span v-if="dirty" class="dirty-hint">有未保存的修改</span>
        <button type="button" class="btn btn-secondary" :disabled="admin.settingsSaving || !dirty" @click="fillFromStore">
          还原
        </button>
        <button type="submit" class="btn btn-primary" :disabled="admin.settingsSaving">
          {{ admin.settingsSaving ? '保存中…' : '保存设置' }}
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  max-width: var(--page-max);
  margin: 0 auto;
}

.page-heading {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.page-title {
  margin: 0;
  font-size: var(--fs-xl);
  font-weight: 600;
  letter-spacing: -0.015em;
}

.page-sub {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.panel {
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
}

.form {
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 18px;
}

.form-block {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-width: 520px;
}

.narrow {
  max-width: 240px;
}

.switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 0;
  border-top: 1px solid var(--color-border);
  border-bottom: 1px solid var(--color-border);
}

.switch-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.switch-row + .switch-row {
  border-top: 0;
}

.switch {
  position: relative;
  display: inline-flex;
  flex: none;
}

.switch input {
  position: absolute;
  inset: 0;
  margin: 0;
  opacity: 0;
  cursor: pointer;
}

.track {
  display: flex;
  align-items: center;
  width: 38px;
  height: 22px;
  padding: 2px;
  border-radius: 999px;
  background: var(--color-border-strong);
  transition: background-color var(--dur) var(--ease);
}

.switch input:checked + .track {
  background: var(--color-primary);
}

.switch input:focus-visible + .track {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.thumb {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: #fff;
  transition: transform var(--dur) var(--ease);
}

.switch input:checked + .track .thumb {
  transform: translateX(16px);
}

.switch.is-disabled {
  cursor: not-allowed;
}

.switch.is-disabled input {
  cursor: not-allowed;
}

.switch.is-disabled .track {
  opacity: 0.45;
}

.readonly {
  padding-top: 16px;
  border-top: 1px solid var(--color-border);
}

.readonly-title {
  margin: 0 0 8px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
}

.readonly-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
  margin: 0;
}

.readonly-grid div {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.readonly-grid dt {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.readonly-grid dd {
  margin: 0;
  font-size: var(--fs-sm);
  font-weight: 600;
}

.form-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 4px;
  border-top: 1px solid var(--color-border);
}

.dirty-hint {
  margin-right: auto;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.state-panel {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  padding: 24px;
}

.state-title {
  margin: 0;
  font-size: var(--fs-base);
  font-weight: 600;
}

.state-copy {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.skeleton-block {
  display: block;
  width: 42%;
  height: 12px;
  border-radius: 4px;
  background: var(--color-border);
  animation: skeleton-pulse 1.2s ease-in-out infinite;
}

.skeleton-block.wide {
  width: 64%;
}

.skeleton-block.short {
  width: 28%;
}

@keyframes skeleton-pulse {
  0%,
  100% {
    opacity: 0.55;
  }
  50% {
    opacity: 1;
  }
}

@media (max-width: 760px) {
  .form {
    padding: 14px;
  }

  .form-block {
    max-width: none;
  }

  .narrow {
    max-width: none;
  }

  .switch-row {
    align-items: flex-start;
  }

  .form-actions {
    flex-wrap: wrap;
  }

  .dirty-hint {
    flex-basis: 100%;
    margin-right: 0;
  }
}
</style>
