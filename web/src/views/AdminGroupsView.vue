<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { addGroupMember, createAdminGroup, deleteAdminGroup, listAdminUsers, listGroupMembers, listAdminGroups, removeGroupMember, updateAdminGroup } from '../api/client'
import type { AdminUser, Group, GroupMember, GroupPermission, GroupRole } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { useGroupsStore } from '../stores/groups'
import { useToast } from '../composables/useToast'
import {
  GROUP_PERMISSIONS,
  groupErrorMessage,
  normalizePermissions,
  validateGroupName,
} from '../lib/groups'
import { formatDateTime } from '../lib/format'
import { errorCodeOf } from '../lib/errors'

const router = useRouter()
const auth = useAuthStore()
const groups = useGroupsStore()
const toast = useToast()

const items = ref<Group[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const errorCode = ref<string | null>(null)

const canSearchUsers = computed(() => auth.isAdmin)
const isDelegateOnly = computed(() => auth.canManageGroups && !auth.isAdmin)

/* --- create -------------------------------------------------------------- */

const createOpen = ref(false)
const createForm = reactive({ name: '', managerId: '', perms: ['read'] as GroupPermission[] })
const createError = ref<string | null>(null)
const createBusy = ref(false)
const managerOptions = ref<AdminUser[]>([])

/* --- edit ---------------------------------------------------------------- */

const editOpen = ref(false)
const editTarget = ref<Group | null>(null)
const editForm = reactive({ name: '', perms: ['read'] as GroupPermission[] })
const editError = ref<string | null>(null)
const editBusy = ref(false)

/* --- delete -------------------------------------------------------------- */

const deleteTarget = ref<Group | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

/* --- members drawer ------------------------------------------------------ */

const membersOpen = ref(false)
const membersTarget = ref<Group | null>(null)
const members = ref<GroupMember[]>([])
const membersLoading = ref(false)
const membersError = ref<string | null>(null)
const memberBusyId = ref<number | null>(null)

const memberQuery = ref('')
const memberResults = ref<AdminUser[]>([])
const memberSearching = ref(false)
const manualUserId = ref('')
const addError = ref<string | null>(null)
const addBusy = ref(false)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorCode.value = null

  try {
    items.value = await listAdminGroups()
  } catch (cause) {
    items.value = []
    errorCode.value = errorCodeOf(cause)
    error.value = groupErrorMessage(cause, '组列表加载失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

function canEdit(group: Group): boolean {
  if (auth.isAdmin) return true
  return group.created_by !== null && group.created_by === auth.user?.id
}

function toggleFormPermission(list: GroupPermission[], permission: GroupPermission): void {
  const index = list.indexOf(permission)
  if (index >= 0) {
    // `read` is mandatory: the server rejects a group without it.
    if (permission === 'read') return
    list.splice(index, 1)
  } else {
    list.push(permission)
    list.sort((a, b) => GROUP_PERMISSIONS.findIndex((item) => item.value === a) - GROUP_PERMISSIONS.findIndex((item) => item.value === b))
  }
}

/* --- create -------------------------------------------------------------- */

async function openCreate(): Promise<void> {
  createForm.name = ''
  createForm.managerId = ''
  createForm.perms = ['read']
  createError.value = null
  createBusy.value = false
  createOpen.value = true
  managerOptions.value = []

  if (auth.isAdmin) {
    try {
      const page = await listAdminUsers({ limit: 100 })
      managerOptions.value = page.items.filter((user) => user.status === 'active')
    } catch {
      // Manager is optional; falling back to "me" is fine.
    }
  }
}

async function submitCreate(): Promise<void> {
  if (createBusy.value) return
  const nameError = validateGroupName(createForm.name)
  if (nameError) {
    createError.value = nameError
    return
  }

  createBusy.value = true
  createError.value = null

  try {
    const created = await createAdminGroup({
      name: createForm.name.trim(),
      member_perms: normalizePermissions(createForm.perms),
      ...(createForm.managerId ? { manager_user_id: Number(createForm.managerId) } : {}),
    })
    createOpen.value = false
    toast.push(`已创建组「${created.group.name}」`)
    await load()
    await groups.load()
  } catch (cause) {
    createError.value = groupErrorMessage(cause, '创建组失败，请稍后重试')
  } finally {
    createBusy.value = false
  }
}

/* --- edit ---------------------------------------------------------------- */

function openEdit(group: Group): void {
  editTarget.value = group
  editForm.name = group.name
  editForm.perms = normalizePermissions(group.member_perms)
  editError.value = null
  editBusy.value = false
  editOpen.value = true
}

async function submitEdit(): Promise<void> {
  const target = editTarget.value
  if (!target || editBusy.value) return
  const nameError = validateGroupName(editForm.name)
  if (nameError) {
    editError.value = nameError
    return
  }

  editBusy.value = true
  editError.value = null

  try {
    const updated = await updateAdminGroup(target.id, {
      name: editForm.name.trim(),
      member_perms: normalizePermissions(editForm.perms),
    })
    editOpen.value = false
    toast.push(`已更新组「${updated.name}」`)
    await load()
    await groups.load()
  } catch (cause) {
    editError.value = groupErrorMessage(cause, '保存失败，请稍后重试')
  } finally {
    editBusy.value = false
  }
}

/* --- delete -------------------------------------------------------------- */

function openDelete(group: Group): void {
  deleteTarget.value = group
  deleteError.value = null
  deleteBusy.value = false
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleteBusy.value) return

  deleteBusy.value = true
  deleteError.value = null

  try {
    await deleteAdminGroup(target.id)
    deleteTarget.value = null
    toast.push(`已删除组「${target.name}」`)
    await load()
    await groups.load()
  } catch (cause) {
    deleteError.value = groupErrorMessage(cause, '删除组失败，请稍后重试')
  } finally {
    deleteBusy.value = false
  }
}

/* --- members ------------------------------------------------------------- */

async function openMembers(group: Group): Promise<void> {
  membersTarget.value = group
  membersOpen.value = true
  membersError.value = null
  addError.value = null
  memberQuery.value = ''
  memberResults.value = []
  manualUserId.value = ''
  await loadMembers()
}

async function loadMembers(): Promise<void> {
  const target = membersTarget.value
  if (!target) return
  membersLoading.value = true
  membersError.value = null

  try {
    members.value = await listGroupMembers(target.id)
  } catch (cause) {
    members.value = []
    membersError.value = groupErrorMessage(cause, '成员加载失败，请稍后重试')
  } finally {
    membersLoading.value = false
  }
}

let searchTimer: number | undefined

function onSearchInput(): void {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    void searchUsers()
  }, 220)
}

async function searchUsers(): Promise<void> {
  const query = memberQuery.value.trim()
  if (!query) {
    memberResults.value = []
    return
  }
  memberSearching.value = true
  try {
    const page = await listAdminUsers({ q: query, limit: 8 })
    const existing = new Set(members.value.map((member) => member.user_id))
    memberResults.value = page.items.filter((user) => !existing.has(user.id))
  } catch (cause) {
    addError.value = groupErrorMessage(cause, '搜索用户失败，请稍后重试')
  } finally {
    memberSearching.value = false
  }
}

async function addMember(userId: number, role: GroupRole = 'member'): Promise<void> {
  const target = membersTarget.value
  if (!target || addBusy.value) return
  if (!Number.isInteger(userId) || userId <= 0) {
    addError.value = '请输入有效的用户 ID'
    return
  }

  addBusy.value = true
  addError.value = null

  try {
    await addGroupMember(target.id, { user_id: userId, role })
    memberQuery.value = ''
    memberResults.value = []
    manualUserId.value = ''
    toast.push('已加入成员')
    await loadMembers()
    await load()
  } catch (cause) {
    addError.value = groupErrorMessage(cause, '添加成员失败，请稍后重试')
  } finally {
    addBusy.value = false
  }
}

async function changeRole(member: GroupMember, role: GroupRole): Promise<void> {
  const target = membersTarget.value
  if (!target || member.role === role || memberBusyId.value !== null) return
  memberBusyId.value = member.user_id

  try {
    await addGroupMember(target.id, { user_id: member.user_id, role })
    toast.push(role === 'manager' ? `已将 ${member.username} 设为管理员` : `已将 ${member.username} 设为组员`)
    await loadMembers()
  } catch (cause) {
    toast.push(groupErrorMessage(cause, '修改角色失败，请稍后重试'), 'error')
  } finally {
    memberBusyId.value = null
  }
}

async function removeMember(member: GroupMember): Promise<void> {
  const target = membersTarget.value
  if (!target || memberBusyId.value !== null) return
  memberBusyId.value = member.user_id

  try {
    await removeGroupMember(target.id, member.user_id)
    toast.push(`已移除 ${member.username}`)
    await loadMembers()
    await load()
  } catch (cause) {
    toast.push(groupErrorMessage(cause, '移除成员失败，请稍后重试'), 'error')
  } finally {
    memberBusyId.value = null
  }
}

async function openGroupFiles(group: Group): Promise<void> {
  // The console only manages groups; content lives in /groups/:id.
  const mine = groups.byId(group.id)
  if (mine) {
    await router.push({ name: 'group', params: { id: String(group.id) } })
    return
  }
  toast.push('加入该组后才能打开组空间', 'error')
}

onMounted(load)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ isDelegateOnly ? '我的组' : '组管理' }}</h1>
        <p class="page-sub">
          {{ isDelegateOnly ? '你只能管理自己创建的组。' : '管理组空间、成员与组员权限。' }}
        </p>
      </div>
      <div class="page-actions">
        <button class="btn btn-secondary" type="button" @click="load">刷新</button>
        <button class="btn btn-primary" type="button" @click="openCreate">新建组</button>
      </div>
    </header>

    <section class="panel" aria-label="组列表">
      <div v-if="loading" class="skeleton" aria-hidden="true">
        <div v-for="index in 4" :key="index" class="skeleton-row" />
      </div>

      <div v-else-if="error" class="state" role="alert">
        <h2 class="state-title">加载失败</h2>
        <p class="state-desc">{{ error }}</p>
        <button class="btn btn-secondary" type="button" @click="load">重试</button>
      </div>

      <div v-else-if="items.length === 0" class="state">
        <h2 class="state-title">还没有组</h2>
        <p class="state-desc">新建一个组，邀请成员一起共享文件。</p>
        <button class="btn btn-primary" type="button" @click="openCreate">新建组</button>
      </div>

      <table v-else class="group-table">
        <thead>
          <tr>
            <th>名称</th>
            <th class="numeric-cell">成员</th>
            <th class="numeric-cell">节点</th>
            <th>创建者</th>
            <th>创建时间</th>
            <th class="actions-cell">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in items" :key="group.id">
            <td class="name-cell">
              <span class="group-name">{{ group.name }}</span>
              <span v-if="!canEdit(group)" class="muted-tag">只读</span>
            </td>
            <td class="numeric-cell">{{ group.member_count }}</td>
            <td class="numeric-cell">{{ group.node_count }}</td>
            <td class="muted-cell">用户 #{{ group.created_by ?? '—' }}</td>
            <td class="muted-cell">{{ formatDateTime(group.created_at) }}</td>
            <td class="actions-cell">
              <button class="text-button" type="button" @click="openGroupFiles(group)">打开</button>
              <button class="text-button" type="button" :disabled="!canEdit(group)" @click="openMembers(group)">
                成员
              </button>
              <button class="text-button" type="button" :disabled="!canEdit(group)" @click="openEdit(group)">
                设置
              </button>
              <button
                class="text-button is-danger"
                type="button"
                :disabled="!canEdit(group)"
                @click="openDelete(group)"
              >
                删除
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <p v-if="isDelegateOnly" class="tip">组空间内容只能通过左侧「组空间」查看；这里只维护成员和权限。</p>

    <!-- create -->
    <Teleport to="body">
      <div v-if="createOpen" class="overlay" @mousedown.self="createOpen = false">
        <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="group-create-title">
          <h2 id="group-create-title" class="dialog-title">新建组</h2>
          <div class="field">
            <label class="field-label" for="group-name">组名称</label>
            <input id="group-name" v-model="createForm.name" class="input" maxlength="64" placeholder="例如：设计部" />
          </div>
          <div class="field">
            <span class="field-label">组员权限（默认只读）</span>
            <div class="perm-grid">
              <label v-for="permission in GROUP_PERMISSIONS" :key="permission.value" class="perm-option">
                <input
                  type="checkbox"
                  :checked="createForm.perms.includes(permission.value)"
                  :disabled="permission.value === 'read'"
                  @change="toggleFormPermission(createForm.perms, permission.value)"
                />
                <span>
                  {{ permission.label }}
                  <small class="perm-hint">{{ permission.hint }}</small>
                </span>
              </label>
            </div>
          </div>
          <div v-if="auth.isAdmin" class="field">
            <label class="field-label" for="group-manager">初始管理员（可选）</label>
            <select id="group-manager" v-model="createForm.managerId" class="input">
              <option value="">我自己</option>
              <option v-for="user in managerOptions" :key="user.id" :value="String(user.id)">
                {{ user.display_name || user.username }}（{{ user.username }}）
              </option>
            </select>
          </div>
          <p v-if="createError" class="dialog-error" role="alert">{{ createError }}</p>
          <div class="dialog-actions">
            <button class="btn btn-secondary" type="button" :disabled="createBusy" @click="createOpen = false">
              取消
            </button>
            <button class="btn btn-primary" type="button" :disabled="createBusy" @click="submitCreate">
              {{ createBusy ? '创建中…' : '创建组' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- edit -->
    <Teleport to="body">
      <div v-if="editOpen && editTarget" class="overlay" @mousedown.self="editOpen = false">
        <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="group-edit-title">
          <h2 id="group-edit-title" class="dialog-title">组设置</h2>
          <div class="field">
            <label class="field-label" for="group-edit-name">组名称</label>
            <input id="group-edit-name" v-model="editForm.name" class="input" maxlength="64" />
          </div>
          <div class="field">
            <span class="field-label">组员权限</span>
            <div class="perm-grid">
              <label v-for="permission in GROUP_PERMISSIONS" :key="permission.value" class="perm-option">
                <input
                  type="checkbox"
                  :checked="editForm.perms.includes(permission.value)"
                  :disabled="permission.value === 'read'"
                  @change="toggleFormPermission(editForm.perms, permission.value)"
                />
                <span>
                  {{ permission.label }}
                  <small class="perm-hint">{{ permission.hint }}</small>
                </span>
              </label>
            </div>
          </div>
          <p v-if="editError" class="dialog-error" role="alert">{{ editError }}</p>
          <div class="dialog-actions">
            <button class="btn btn-secondary" type="button" :disabled="editBusy" @click="editOpen = false">取消</button>
            <button class="btn btn-primary" type="button" :disabled="editBusy" @click="submitEdit">
              {{ editBusy ? '保存中…' : '保存' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- members drawer -->
    <Teleport to="body">
      <div v-if="membersOpen && membersTarget" class="overlay" @mousedown.self="membersOpen = false">
        <div class="drawer" role="dialog" aria-modal="true" aria-labelledby="members-title">
          <div class="drawer-head">
            <div>
              <h2 id="members-title" class="dialog-title">成员 · {{ membersTarget.name }}</h2>
              <p class="dialog-sub">组员按组配置的权限访问，管理员拥有全部权限。</p>
            </div>
            <button class="icon-button" type="button" aria-label="关闭" @click="membersOpen = false">
              <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
              </svg>
            </button>
          </div>

          <div v-if="membersLoading" class="skeleton" aria-hidden="true">
            <div v-for="index in 3" :key="index" class="skeleton-row" />
          </div>
          <p v-else-if="membersError" class="dialog-error" role="alert">{{ membersError }}</p>
          <ul v-else class="member-list">
            <li v-for="member in members" :key="member.user_id" class="member-row">
              <div class="member-main">
                <span class="member-name">{{ member.display_name || member.username }}</span>
                <span class="member-username">@{{ member.username }} · ID {{ member.user_id }}</span>
              </div>
              <select
                class="input member-role"
                :value="member.role"
                :disabled="memberBusyId !== null"
                :aria-label="`${member.username} 的角色`"
                @change="changeRole(member, ($event.target as HTMLSelectElement).value as GroupRole)"
              >
                <option value="manager">管理员</option>
                <option value="member">组员</option>
              </select>
              <button
                class="text-button is-danger"
                type="button"
                :disabled="memberBusyId !== null"
                @click="removeMember(member)"
              >
                移除
              </button>
            </li>
          </ul>

          <div class="add-member">
            <template v-if="canSearchUsers">
              <label class="field-label" for="member-search">添加成员</label>
              <input
                id="member-search"
                v-model="memberQuery"
                class="input"
                type="search"
                placeholder="搜索用户名"
                @input="onSearchInput"
              />
              <p v-if="memberSearching" class="dialog-hint">搜索中…</p>
              <ul v-if="memberResults.length > 0" class="search-results">
                <li v-for="user in memberResults" :key="user.id" class="search-result">
                  <span>{{ user.display_name || user.username }}（{{ user.username }}）</span>
                  <button class="btn btn-secondary btn-small" type="button" :disabled="addBusy" @click="addMember(user.id)">
                    加入
                  </button>
                </li>
              </ul>
            </template>
            <template v-else>
              <label class="field-label" for="member-id">添加成员（用户 ID）</label>
              <div class="manual-row">
                <input id="member-id" v-model="manualUserId" class="input" inputmode="numeric" placeholder="用户 ID" />
                <button class="btn btn-secondary" type="button" :disabled="addBusy" @click="addMember(Number(manualUserId))">
                  加入
                </button>
              </div>
              <p class="dialog-hint">委派管理员看不到用户列表，请向用户索取其 ID。</p>
            </template>
            <p v-if="addError" class="dialog-error" role="alert">{{ addError }}</p>
          </div>

          <p class="dialog-hint">至少保留一个组管理员；移除或降级最后一个管理员会被拒绝。</p>
          <div class="dialog-actions">
            <button class="btn btn-secondary" type="button" @click="membersOpen = false">关闭</button>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除组？"
      :message="`「${deleteTarget?.name ?? ''}」会被删除，成员将失去访问权限。`"
      detail="仅空组可删：请先清空组空间并撤销相关分享。此操作不可恢复。"
      confirm-label="删除组"
      danger
      :busy="deleteBusy"
      :error="deleteError"
      @close="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>

<style scoped>
.page {
  min-height: max(320px, calc(100vh - 264px));
}

.page-head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 18px;
}

.page-title {
  margin: 0;
  font-size: var(--fs-xl);
  font-weight: 600;
  letter-spacing: -0.015em;
}

.page-sub {
  margin-top: 2px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.page-actions {
  display: flex;
  gap: 8px;
}

.panel {
  overflow-x: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
}

.group-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--fs-sm);
}

.group-table thead th {
  padding: 10px 14px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg);
  color: var(--color-muted);
  font-size: var(--fs-xs);
  font-weight: 600;
  text-align: left;
  white-space: nowrap;
}

.group-table tbody td {
  padding: 11px 14px;
  border-top: 1px solid var(--color-border);
  vertical-align: middle;
}

.group-table tbody tr:first-child td {
  border-top: 0;
}

.name-cell {
  min-width: 140px;
  font-weight: 600;
}

.group-name {
  margin-right: 6px;
}

.muted-tag {
  padding: 1px 6px;
  border-radius: var(--radius-badge);
  background: var(--color-bg);
  color: var(--color-muted);
  font-size: 11px;
  font-weight: 500;
}

.numeric-cell {
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.perms-cell {
  max-width: 240px;
}

.perm-tag {
  display: inline-block;
  margin: 1px 3px 1px 0;
  padding: 1px 6px;
  border-radius: var(--radius-badge);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-size: 11px;
}

.muted-cell {
  color: var(--color-muted);
  white-space: nowrap;
}

.actions-cell {
  white-space: nowrap;
  text-align: right;
}

.text-button {
  padding: 2px 4px;
  color: var(--color-primary-ink);
  font-size: var(--fs-sm);
}

.text-button:hover {
  text-decoration: underline;
}

.text-button:disabled {
  color: var(--color-muted);
  text-decoration: none;
}

.text-button.is-danger {
  color: var(--color-danger);
}

.text-button.is-danger:disabled {
  color: var(--color-muted);
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 48px 24px;
  text-align: center;
}

.state-title {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
}

.state-desc {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.skeleton {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.skeleton-row {
  height: 46px;
  background: var(--color-border);
  opacity: 0.3;
}

.tip {
  margin-top: 12px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

/* dialogs */
.overlay {
  position: fixed;
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: var(--color-overlay);
  inset: 0;
}

.dialog {
  width: min(480px, 100%);
  max-height: min(88vh, 760px);
  overflow-y: auto;
  padding: 22px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
  box-shadow: var(--shadow-pop);
}

.drawer {
  width: min(560px, 100%);
  max-height: min(88vh, 780px);
  overflow-y: auto;
  padding: 20px 22px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
  box-shadow: var(--shadow-pop);
}

.drawer-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.dialog-title {
  margin: 0 0 4px;
  font-size: var(--fs-lg);
  font-weight: 600;
}

.dialog-sub {
  margin: 0 0 14px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 14px;
}

.field-label {
  font-size: var(--fs-sm);
  font-weight: 500;
}

.input {
  width: 100%;
  height: 36px;
  padding: 0 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  color: var(--color-text);
  font-size: var(--fs-sm);
}

.input:focus {
  border-color: var(--color-primary);
  outline: none;
  box-shadow: 0 0 0 3px var(--color-focus-soft);
}

.perm-grid {
  display: grid;
  gap: 6px;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
}

.perm-option {
  display: flex;
  align-items: flex-start;
  gap: 7px;
  padding: 7px 9px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font-size: var(--fs-sm);
  cursor: pointer;
}

.perm-option input {
  margin-top: 2px;
  accent-color: var(--color-primary);
}

.perm-hint {
  display: block;
  color: var(--color-muted);
  font-size: 11px;
}

.dialog-error {
  margin: 4px 0 8px;
  padding: 7px 9px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-soft);
  color: var(--color-danger);
  font-size: var(--fs-xs);
}

.dialog-hint {
  margin: 6px 0 0;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}

.icon-button {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
}

.icon-button:hover {
  background: var(--color-bg);
  color: var(--color-text);
}

.member-list {
  display: flex;
  flex-direction: column;
  margin: 14px 0;
  padding: 0;
  list-style: none;
  gap: 1px;
}

.member-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 0;
  border-bottom: 1px solid var(--color-border);
}

.member-main {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.member-name {
  font-size: var(--fs-sm);
  font-weight: 500;
}

.member-username {
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.member-role {
  width: 110px;
  height: 32px;
}

.add-member {
  margin-top: 14px;
}

.manual-row {
  display: flex;
  gap: 8px;
}

.search-results {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}

.search-result {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 7px 0;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--fs-sm);
}

.btn-small {
  height: 28px;
  padding: 0 9px;
  font-size: var(--fs-xs);
}

@media (max-width: 700px) {
  .group-table thead {
    display: none;
  }

  .group-table tbody tr {
    display: grid;
    gap: 4px;
    padding: 10px 12px;
    border-top: 1px solid var(--color-border);
  }

  .group-table tbody td {
    padding: 0;
    border: 0;
    text-align: left;
  }

  .actions-cell {
    padding-top: 4px;
    text-align: left;
  }

  .member-row {
    flex-wrap: wrap;
  }
}
</style>
