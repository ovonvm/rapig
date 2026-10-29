<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useToast } from '../composables/useToast'
import { copyText } from '../lib/clipboard'
import { formatDateTime } from '../lib/format'
import { normalizeEmail, normalizePhone, validateEmailField, validatePhoneField } from '../lib/account'
import {
  adminErrorMessage,
  bytesToGbText,
  deletionRemainingLabel,
  parseGbInput,
  roleLabel,
  statusLabel,
  usageText,
} from '../lib/admin'
import { useAdminStore } from '../stores/admin'
import { useAuthStore } from '../stores/auth'
import type { AdminRole, AdminUser } from '../api/client'

const admin = useAdminStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const toast = useToast()

const ROLE_OPTIONS: AdminRole[] = ['user', 'admin']
const USERNAME_MAX = 32
const PASSWORD_MIN = 8

const qInput = ref('')
const pageError = ref<string | null>(null)
const loadingMore = ref(false)

const selfId = computed(() => auth.user?.id ?? 0)

function isSelf(user: AdminUser): boolean {
  return user.id === selfId.value
}

/* ------------------------------------------------ deletion status (M5.2c) */
type DeletionFilter = 'all' | 'deleting'

const deletionFilter = ref<DeletionFilter>('all')
const restoringId = ref(0)

const deletingCount = computed(() => admin.users.filter((user) => Boolean(user.deleted_at)).length)
const visibleUsers = computed(() =>
  deletionFilter.value === 'deleting'
    ? admin.users.filter((user) => Boolean(user.deleted_at))
    : admin.users,
)

function isDeleting(user: AdminUser): boolean {
  return Boolean(user.deleted_at)
}

function deletionBadge(user: AdminUser): string {
  return deletionRemainingLabel(user.purge_after) ?? '注销中'
}

/* ------------------------------------------------------------- search */
async function submitSearch(): Promise<void> {
  const q = qInput.value.trim()
  await router.replace({ name: 'admin-users', query: q ? { q } : {} })
  await admin.loadUsers(q).catch(() => undefined)
}

/* -------------------------------------------------------- create user */
interface CreateForm {
  username: string
  email: string
  phone: string
  password: string
  role: AdminRole
  quotaGb: string
}

const createOpen = ref(false)
const createBusy = ref(false)
const createError = ref<string | null>(null)
const createForm = reactive<CreateForm>({
  username: '',
  email: '',
  phone: '',
  password: '',
  role: 'user',
  quotaGb: '0',
})
const usernameRef = ref<HTMLInputElement | null>(null)

const createQuotaBytes = computed(() => parseGbInput(createForm.quotaGb))

function openCreate(): void {
  createForm.username = ''
  createForm.email = ''
  createForm.phone = ''
  createForm.password = ''
  createForm.role = 'user'
  createForm.quotaGb = '0'
  createError.value = null
  createOpen.value = true
  void nextTick(() => usernameRef.value?.focus())
}

function validateCreate(): string | null {
  const username = createForm.username.trim()
  if (username.length < 2 || username.length > USERNAME_MAX) return `用户名需为 2–${USERNAME_MAX} 个字符`
  const emailProblem = validateEmailField(createForm.email)
  if (emailProblem) return emailProblem
  const phoneProblem = validatePhoneField(createForm.phone)
  if (phoneProblem) return phoneProblem
  if (createForm.password && createForm.password.length < PASSWORD_MIN) return `密码至少需要 ${PASSWORD_MIN} 个字符`
  if (createQuotaBytes.value === null) return '配额请填写不小于 0 的数字（GB，0 表示不限）'
  return null
}

async function submitCreate(): Promise<void> {
  const problem = validateCreate()
  if (problem) {
    createError.value = problem
    return
  }
  createBusy.value = true
  createError.value = null
  try {
    const email = normalizeEmail(createForm.email)
    const phone = normalizePhone(createForm.phone)
    const password = await admin.createUser({
      username: createForm.username.trim(),
      password: createForm.password || undefined,
      role: createForm.role,
      quota_bytes: createQuotaBytes.value ?? 0,
      ...(email ? { email } : {}),
      ...(phone ? { phone } : {}),
    })
    createOpen.value = false
    if (password) {
      oneTime.value = { open: true, title: '初始密码', username: createForm.username.trim(), password }
    } else {
      toast.push(`已创建用户「${createForm.username.trim()}」`)
    }
  } catch (cause) {
    createError.value = adminErrorMessage(cause, '创建用户失败，请稍后重试')
  } finally {
    createBusy.value = false
  }
}

/* --------------------------------------------------------- edit dialog */
const editOpen = ref(false)
const editBusy = ref(false)
const editError = ref<string | null>(null)
const editUser = ref<AdminUser | null>(null)
const editForm = reactive<{ role: AdminRole; quotaGb: string; canCreateGroups: boolean }>({
  role: 'user',
  quotaGb: '0',
  canCreateGroups: false,
})

const editQuotaBytes = computed(() => parseGbInput(editForm.quotaGb))
const editSelf = computed(() => (editUser.value ? isSelf(editUser.value) : false))

function openEdit(user: AdminUser): void {
  editUser.value = user
  editForm.role = user.role === 'admin' ? 'admin' : 'user'
  editForm.quotaGb = bytesToGbText(user.quota_bytes)
  editForm.canCreateGroups = user.can_create_groups === true
  editError.value = null
  editOpen.value = true
}

async function submitEdit(): Promise<void> {
  if (!editUser.value) return
  if (editQuotaBytes.value === null) {
    editError.value = '配额请填写不小于 0 的数字（GB，0 表示不限）'
    return
  }
  editBusy.value = true
  editError.value = null
  const input: { quota_bytes: number; role?: AdminRole; can_create_groups?: boolean } = {
    quota_bytes: editQuotaBytes.value,
  }
  if (!editSelf.value) input.role = editForm.role
  // Only system admins may grant or revoke group-creation rights (can_create_groups).
  if (auth.isAdmin) input.can_create_groups = editForm.canCreateGroups
  try {
    const updated = await admin.patchUser(editUser.value.id, input)
    editOpen.value = false
    toast.push(`已更新用户「${updated.username}」`)
  } catch (cause) {
    editError.value = adminErrorMessage(cause, '更新用户失败，请稍后重试')
  } finally {
    editBusy.value = false
  }
}

/* --------------------------------------------------- confirm + one-time */
interface ConfirmState {
  open: boolean
  kind: 'status' | 'reset' | null
  user: AdminUser | null
  busy: boolean
  error: string | null
}

const confirm = reactive<ConfirmState>({ open: false, kind: null, user: null, busy: false, error: null })

const confirmTitle = computed(() => {
  if (!confirm.user) return ''
  if (confirm.kind === 'reset') return '重置密码？'
  return confirm.user.status === 'active' ? '禁用该用户？' : '启用该用户？'
})

const confirmMessage = computed(() => {
  if (!confirm.user) return ''
  if (confirm.kind === 'reset') return `「${confirm.user.username}」的密码将被重置，该用户的所有登录会话会立即失效。`
  return confirm.user.status === 'active'
    ? `「${confirm.user.username}」将无法登录，已上传的文件会保留。`
    : `「${confirm.user.username}」将恢复登录能力。`
})

function openConfirm(user: AdminUser, kind: 'status' | 'reset'): void {
  confirm.open = true
  confirm.kind = kind
  confirm.user = user
  confirm.busy = false
  confirm.error = null
}

async function runConfirm(): Promise<void> {
  const user = confirm.user
  if (!user || !confirm.kind) return
  confirm.busy = true
  confirm.error = null
  try {
    if (confirm.kind === 'reset') {
      const password = await admin.resetPassword(user.id)
      confirm.open = false
      oneTime.value = { open: true, title: '新密码', username: user.username, password }
    } else {
      const next = user.status === 'active' ? 'disabled' : 'active'
      await admin.patchUser(user.id, { status: next })
      confirm.open = false
      toast.push(next === 'disabled' ? `已禁用「${user.username}」` : `已启用「${user.username}」`)
    }
  } catch (cause) {
    confirm.error = adminErrorMessage(cause, '操作失败，请稍后重试')
  } finally {
    confirm.busy = false
  }
}

interface OneTimeState {
  open: boolean
  title: string
  username: string
  password: string
}
const oneTime = ref<OneTimeState>({ open: false, title: '', username: '', password: '' })
const copied = ref(false)

async function copyOneTime(): Promise<void> {
  const ok = await copyText(oneTime.value.password)
  copied.value = ok
  if (ok) toast.push('密码已复制')
}

function closeOneTime(): void {
  oneTime.value = { open: false, title: '', username: '', password: '' }
  copied.value = false
}

/* ------------------------------------------ restore / purge (M5.2c) */
async function restoreUser(user: AdminUser): Promise<void> {
  if (restoringId.value !== 0) return
  restoringId.value = user.id
  try {
    const restored = await admin.restoreUser(user.id)
    toast.push(`已恢复账号「${restored.username}」`)
  } catch (cause) {
    toast.push(adminErrorMessage(cause, '恢复账号失败，请稍后重试'), 'error')
  } finally {
    restoringId.value = 0
  }
}

interface PurgeState {
  open: boolean
  user: AdminUser | null
  busy: boolean
  error: string | null
}

const purge = reactive<PurgeState>({ open: false, user: null, busy: false, error: null })

function openPurge(user: AdminUser): void {
  purge.open = true
  purge.user = user
  purge.busy = false
  purge.error = null
}

async function runPurge(): Promise<void> {
  const user = purge.user
  if (!user) return
  purge.busy = true
  purge.error = null
  try {
    await admin.purgeUser(user.id)
    purge.open = false
    toast.push(`已永久删除用户「${user.username}」`)
  } catch (cause) {
    purge.error = adminErrorMessage(cause, '删除用户失败，请稍后重试')
  } finally {
    purge.busy = false
  }
}

/* ----------------------------------------------------------- pagination */
async function loadMore(): Promise<void> {
  loadingMore.value = true
  try {
    await admin.loadMoreUsers()
  } catch (cause) {
    toast.push(adminErrorMessage(cause, '加载更多失败，请稍后重试'), 'error')
  } finally {
    loadingMore.value = false
  }
}

async function retry(): Promise<void> {
  pageError.value = null
  await refresh()
}

async function refresh(): Promise<void> {
  try {
    await admin.loadUsers()
    pageError.value = null
  } catch (cause) {
    pageError.value = adminErrorMessage(cause, '用户列表加载失败，请稍后重试')
  }
}

onMounted(() => {
  const raw = route.query.q
  const initial = typeof raw === 'string' ? raw : ''
  qInput.value = initial
  void admin.loadUsers(initial)
  if (admin.usersQuery !== initial) admin.usersQuery = initial
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">用户管理</h1>
        <p class="page-sub">共 {{ admin.usersTotal }} 个账号</p>
      </div>
      <div class="page-actions">
        <label class="filter">
          <span class="visually-hidden">筛选注销状态</span>
          <select v-model="deletionFilter" class="input filter-select" aria-label="筛选注销状态">
            <option value="all">全部账号</option>
            <option value="deleting">注销中（{{ deletingCount }}）</option>
          </select>
        </label>
        <form class="search" role="search" @submit.prevent="submitSearch">
          <svg width="15" height="15" viewBox="0 0 20 20" aria-hidden="true">
            <circle cx="8.6" cy="8.6" r="4.6" fill="none" stroke="currentColor" stroke-width="1.4" />
            <path d="m12.2 12.2 3.4 3.4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
          </svg>
          <input
            v-model="qInput"
            class="search-input"
            type="search"
            name="q"
            placeholder="搜索用户名"
            aria-label="搜索用户名"
          />
        </form>
        <button type="button" class="btn btn-primary" @click="openCreate">
          <svg width="15" height="15" viewBox="0 0 20 20" aria-hidden="true">
            <path d="M10 4.4v11.2M4.4 10h11.2" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
          </svg>
          创建用户
        </button>
      </div>
    </header>

    <section class="panel" aria-label="用户列表">
      <div class="table-scroll">
        <table class="table">
          <thead>
            <tr>
              <th scope="col">用户名</th>
              <th scope="col">标识</th>
              <th scope="col">显示名</th>
              <th scope="col">角色</th>
              <th scope="col">状态</th>
              <th scope="col">已用 / 配额</th>
              <th scope="col">创建时间</th>
              <th scope="col" class="col-actions">操作</th>
            </tr>
          </thead>

          <tbody v-if="admin.usersLoading && admin.users.length === 0">
            <tr v-for="row in 4" :key="row" class="skeleton-row">
              <td v-for="col in 8" :key="col"><span class="skeleton-block"></span></td>
            </tr>
          </tbody>

          <tbody v-else-if="admin.usersError">
            <tr>
              <td colspan="8">
                <div class="state" role="alert">
                  <p class="state-title">加载失败</p>
                  <p class="state-copy">{{ admin.usersError }}</p>
                  <button type="button" class="btn btn-secondary" @click="retry">重试</button>
                </div>
              </td>
            </tr>
          </tbody>

          <tbody v-else-if="admin.users.length === 0">
            <tr>
              <td colspan="8">
                <div class="state">
                  <p class="state-title">没有匹配的用户</p>
                  <p class="state-copy">换个关键词，或者清空搜索查看全部账号。</p>
                </div>
              </td>
            </tr>
          </tbody>

          <tbody v-else>
            <tr v-if="visibleUsers.length === 0">
              <td colspan="8">
                <div class="state">
                  <p class="state-title">没有注销中的账号</p>
                  <p class="state-copy">切回「全部账号」查看所有用户。</p>
                </div>
              </td>
            </tr>
            <tr v-for="user in visibleUsers" :key="user.id">
              <td>
                <div class="cell-name">
                  <span class="user-name">{{ user.username }}</span>
                  <span v-if="user.is_root" class="root-badge">root</span>
                  <span v-if="isSelf(user)" class="self-badge">你</span>
                  <span v-if="user.locked_until" class="lock-badge">临时锁定</span>
                </div>
              </td>
              <td>
                <div class="cell-identifier">
                  <span class="identifier-line">{{ user.email || '—' }}</span>
                  <span v-if="user.phone" class="identifier-line">{{ user.phone }}</span>
                </div>
              </td>
              <td class="cell-muted">{{ user.display_name || '—' }}</td>
              <td>
                <span class="role-badge" :class="user.role === 'admin' ? 'is-admin' : ''">{{ roleLabel(user.role) }}</span>
              </td>
              <td>
                <span
                  v-if="isDeleting(user)"
                  class="status-badge is-deleting"
                  :title="user.deleted_at ? `申请于 ${formatDateTime(user.deleted_at)}` : ''"
                >
                  <span class="dot" aria-hidden="true"></span>{{ deletionBadge(user) }}
                </span>
                <span v-else class="status-badge" :class="user.status === 'active' ? 'is-active' : 'is-disabled'">
                  <span class="dot" aria-hidden="true"></span>{{ statusLabel(user.status) }}
                </span>
              </td>
              <td class="cell-muted numeric">{{ usageText(user.used_bytes, user.quota_bytes) }}</td>
              <td class="cell-muted">{{ formatDateTime(user.created_at) }}</td>
              <td class="col-actions">
                <div class="row-actions">
                  <template v-if="isDeleting(user)">
                    <button
                      type="button"
                      class="text-button"
                      :disabled="restoringId === user.id"
                      @click="restoreUser(user)"
                    >
                      {{ restoringId === user.id ? '恢复中…' : '恢复账号' }}
                    </button>
                    <button
                      type="button"
                      class="text-button is-danger"
                      :disabled="isSelf(user) || Boolean(user.is_root)"
                      :title="
                        user.is_root
                          ? 'root 管理员不能删除'
                          : isSelf(user)
                            ? '不能对自己做此操作'
                            : '永久删除后无法恢复'
                      "
                      @click="openPurge(user)"
                    >
                      立即永久删除
                    </button>
                  </template>
                  <template v-else>
                    <button type="button" class="text-button" @click="openEdit(user)">编辑</button>
                    <button
                      type="button"
                      class="text-button"
                      :class="{ 'is-danger': user.status === 'active' }"
                      :disabled="isSelf(user)"
                      :title="isSelf(user) ? '不能对自己做此操作' : ''"
                      @click="openConfirm(user, 'status')"
                    >
                      {{ user.status === 'active' ? '禁用' : '启用' }}
                    </button>
                    <button
                      type="button"
                      class="text-button"
                      :disabled="isSelf(user)"
                      :title="isSelf(user) ? '不能对自己做此操作' : ''"
                      @click="openConfirm(user, 'reset')"
                    >
                      重置密码
                    </button>
                  </template>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="admin.hasMoreUsers" class="load-more">
        <button type="button" class="btn btn-secondary" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? '加载中…' : `加载更多（还有 ${admin.usersTotal - admin.users.length} 个）` }}
        </button>
      </div>
    </section>

    <!-- create -->
    <Teleport to="body">
      <Transition name="dialog">
        <div v-if="createOpen" class="overlay" @mousedown.self="createOpen = false">
          <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="create-user-title">
            <h2 id="create-user-title" class="dialog-title">创建用户</h2>
            <p class="dialog-sub">账号创建后会立即生效，管理员可随时禁用或重置密码。</p>
            <form class="dialog-form" novalidate @submit.prevent="submitCreate">
              <label class="field">
                <span class="field-label">用户名</span>
                <input
                  ref="usernameRef"
                  v-model="createForm.username"
                  class="input"
                  type="text"
                  autocomplete="off"
                  maxlength="32"
                  placeholder="2–32 个字符"
                  :aria-invalid="Boolean(createError)"
                />
              </label>
              <label class="field">
                <span class="field-label">初始密码</span>
                <input
                  v-model="createForm.password"
                  class="input"
                  type="password"
                  autocomplete="new-password"
                  placeholder="留空自动生成一次性密码"
                />
                <span class="field-hint">留空时由系统生成一次性密码，创建后只显示一次。</span>
              </label>
              <div class="field-row">
                <label class="field">
                  <span class="field-label">邮箱（选填）</span>
                  <input v-model="createForm.email" class="input" type="email" autocomplete="off" placeholder="user@example.com" />
                </label>
                <label class="field">
                  <span class="field-label">手机号（选填）</span>
                  <input v-model="createForm.phone" class="input" type="tel" autocomplete="off" placeholder="可带 + 前缀" />
                </label>
              </div>
              <div class="field-row">
                <label class="field">
                  <span class="field-label">角色</span>
                  <select v-model="createForm.role" class="input">
                    <option v-for="role in ROLE_OPTIONS" :key="role" :value="role">{{ roleLabel(role) }}</option>
                  </select>
                </label>
                <label class="field">
                  <span class="field-label">配额（GB）</span>
                  <input v-model="createForm.quotaGb" class="input" type="text" inputmode="decimal" placeholder="0" />
                  <span class="field-hint">0 表示不限</span>
                </label>
              </div>
              <p v-if="createError" class="field-error" role="alert">{{ createError }}</p>
              <div class="dialog-actions">
                <button type="button" class="btn btn-secondary" :disabled="createBusy" @click="createOpen = false">
                  取消
                </button>
                <button type="submit" class="btn btn-primary" :disabled="createBusy">
                  {{ createBusy ? '创建中…' : '创建' }}
                </button>
              </div>
            </form>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- edit -->
    <Teleport to="body">
      <Transition name="dialog">
        <div v-if="editOpen" class="overlay" @mousedown.self="editOpen = false">
          <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="edit-user-title">
            <h2 id="edit-user-title" class="dialog-title">编辑「{{ editUser?.username }}」</h2>
            <p v-if="editSelf" class="dialog-sub">这是你自己的账号：角色不可修改，配额可以调整。</p>
            <form class="dialog-form" novalidate @submit.prevent="submitEdit">
              <label class="field">
                <span class="field-label">角色</span>
                <select
                  v-model="editForm.role"
                  class="input"
                  :disabled="editSelf"
                  :title="editSelf ? '不能对自己做此操作' : ''"
                >
                  <option v-for="role in ROLE_OPTIONS" :key="role" :value="role">{{ roleLabel(role) }}</option>
                </select>
              </label>
              <label class="field">
                <span class="field-label">配额（GB）</span>
                <input v-model="editForm.quotaGb" class="input" type="text" inputmode="decimal" placeholder="0" />
                <span class="field-hint">0 表示不限；按十进制 GB 换算。</span>
              </label>
              <label v-if="auth.isAdmin" class="edit-switch">
                <span class="edit-switch-text">
                  可创建组
                  <span class="field-hint">开启后可在「组管理」中创建并管理自己的组。</span>
                </span>
                <input
                  v-model="editForm.canCreateGroups"
                  class="edit-switch-input"
                  type="checkbox"
                  :aria-label="`允许 ${editUser?.username ?? ''} 创建组`"
                />
              </label>
              <p v-if="editError" class="field-error" role="alert">{{ editError }}</p>
              <div class="dialog-actions">
                <button type="button" class="btn btn-secondary" :disabled="editBusy" @click="editOpen = false">
                  取消
                </button>
                <button type="submit" class="btn btn-primary" :disabled="editBusy">
                  {{ editBusy ? '保存中…' : '保存' }}
                </button>
              </div>
            </form>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- one-time password -->
    <Teleport to="body">
      <Transition name="dialog">
        <div v-if="oneTime.open" class="overlay" @mousedown.self="closeOneTime">
          <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="onetime-title">
            <h2 id="onetime-title" class="dialog-title">{{ oneTime.title }}</h2>
            <p class="dialog-sub">「{{ oneTime.username }}」的一次性密码：</p>
            <div class="onetime">
              <input
                class="input onetime-input"
                :value="oneTime.password"
                type="text"
                readonly
                aria-label="一次性密码"
                @focus="($event.target as HTMLInputElement).select()"
              />
              <button type="button" class="btn btn-secondary" @click="copyOneTime">
                {{ copied ? '已复制' : '复制' }}
              </button>
            </div>
            <p class="onetime-note" role="alert">该密码只显示这一次，关闭后无法再次查看，请立即交给用户并提示其修改。</p>
            <div class="dialog-actions">
              <button type="button" class="btn btn-primary" @click="closeOneTime">我已保存</button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>

    <ConfirmDialog
      :open="confirm.open"
      :title="confirmTitle"
      :message="confirmMessage"
      :detail="confirm.kind === 'reset' ? '用户下次登录时需要使用新密码。' : ''"
      :confirm-label="confirm.kind === 'reset' ? '重置密码' : '确定'"
      :danger="confirm.kind === 'reset' || confirm.user?.status === 'active'"
      :busy="confirm.busy"
      :error="confirm.error"
      @close="confirm.open = false"
      @confirm="runConfirm"
    />

    <ConfirmDialog
      :open="purge.open"
      :title="purge.user ? `永久删除「${purge.user.username}」？` : '永久删除用户'"
      message="该账号及其名下的所有文件会被立即彻底删除，无法恢复。"
      detail="只有在账号处于注销宽限期时才能永久删除。"
      confirm-label="永久删除"
      danger
      :require-text="purge.user?.username ?? ''"
      :busy="purge.busy"
      :error="purge.error"
      @close="purge.open = false"
      @confirm="runPurge"
    />
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

.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
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

.page-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.filter-select {
  width: auto;
  min-width: 132px;
  height: 34px;
  padding: 0 8px;
  font-size: var(--fs-sm);
}

.search {
  display: flex;
  align-items: center;
  gap: 7px;
  height: 34px;
  padding: 0 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  color: var(--color-muted);
}

.search:focus-within {
  border-color: var(--color-primary);
}

.search-input {
  width: 168px;
  border: 0;
  background: none;
  color: var(--color-text);
  font-size: var(--fs-sm);
  outline: none;
}

.panel {
  position: relative;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
  padding: 14px 14px 0;
}

.table-scroll {
  overflow-x: auto;
}

.table {
  width: 100%;
  min-width: 860px;
  border-collapse: collapse;
  text-align: left;
}

.table th {
  padding: 8px 10px 10px;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-muted);
  font-size: var(--fs-xs);
  font-weight: 500;
  white-space: nowrap;
}

.table td {
  padding: 11px 10px;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-sm);
  vertical-align: middle;
}

.table tbody tr:last-child td {
  border-bottom: 0;
}

.cell-name {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 500;
}

.self-badge,
.lock-badge,
.role-badge,
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 7px;
  border-radius: var(--radius-badge);
  font-size: var(--fs-xs);
  font-weight: 500;
  white-space: nowrap;
}

.self-badge {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.root-badge {
  display: inline-flex;
  align-items: center;
  padding: 1px 7px;
  font-size: var(--fs-xs);
  font-weight: 500;
  color: var(--color-primary-ink);
  background: var(--color-primary-soft);
  border: 1px solid color-mix(in srgb, var(--color-primary) 35%, transparent);
  border-radius: var(--radius-badge);
  white-space: nowrap;
}

.cell-identifier {
  display: flex;
  flex-direction: column;
  gap: 1px;
  max-width: 200px;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.identifier-line {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.lock-badge {
  background: var(--color-warning-soft);
  color: var(--color-text);
}

.role-badge {
  background: var(--color-bg);
  color: var(--color-muted);
}

.role-badge.is-admin {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.status-badge {
  background: var(--color-bg);
  color: var(--color-muted);
}

.status-badge.is-active {
  background: var(--color-success-soft);
  color: var(--color-success);
}

.status-badge.is-disabled {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.status-badge.is-deleting {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: currentColor;
}

.cell-muted {
  color: var(--color-muted);
}

.col-actions {
  text-align: right;
}

.row-actions {
  display: flex;
  justify-content: flex-end;
  gap: 2px;
}

.text-button {
  padding: 3px 6px;
  border-radius: var(--radius-sm);
  color: var(--color-primary-ink);
  font-size: var(--fs-xs);
  font-weight: 500;
}

.text-button:hover:not(:disabled) {
  background: var(--color-primary-soft);
}

.text-button:disabled {
  color: var(--color-muted);
  opacity: 0.6;
  cursor: not-allowed;
}

.text-button.is-danger {
  color: var(--color-danger);
}

.text-button.is-danger:disabled {
  color: var(--color-muted);
}

.text-button.is-danger:hover:not(:disabled) {
  background: var(--color-danger-soft);
}

.load-more {
  display: flex;
  justify-content: center;
  padding: 12px 0 14px;
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 34px 16px;
  text-align: center;
}

.state-title {
  margin: 0;
  font-size: var(--fs-base);
  font-weight: 600;
}

.state-copy {
  margin: 0 0 4px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.skeleton-block {
  display: block;
  width: 72%;
  height: 10px;
  border-radius: 4px;
  background: var(--color-border);
  animation: skeleton-pulse 1.2s ease-in-out infinite;
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

/* dialogs */
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
  max-height: min(90vh, 720px);
  overflow: auto;
  padding: 20px 22px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
  box-shadow: var(--shadow-pop);
}

.dialog-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
}

.dialog-sub {
  margin: 4px 0 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.dialog-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 16px;
}

.field-row {
  display: flex;
  gap: 10px;
}

.field-row .field {
  flex: 1;
  min-width: 0;
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}

.onetime {
  display: flex;
  gap: 8px;
  margin-top: 10px;
}

.onetime-input {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Consolas, monospace);
}

.onetime-note {
  margin: 10px 0 0;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-soft);
  color: var(--color-danger);
  font-size: var(--fs-xs);
}

.dialog-enter-active,
.dialog-leave-active {
  transition: opacity 140ms var(--ease);
}

.dialog-enter-from,
.dialog-leave-to {
  opacity: 0;
}

@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    align-items: stretch;
  }

  .page-actions {
    flex-direction: column;
    align-items: stretch;
  }

  .search {
    width: 100%;
  }

  .search-input {
    width: 100%;
    flex: 1;
  }

  .field-row {
    flex-direction: column;
  }
}

.edit-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 9px 11px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.edit-switch-text {
  display: flex;
  flex-direction: column;
  font-size: var(--fs-sm);
  font-weight: 500;
}

.edit-switch-input {
  width: 16px;
  height: 16px;
  flex: none;
  margin: 0;
  accent-color: var(--color-primary);
  cursor: pointer;
}
</style>
