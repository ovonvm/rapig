<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { useToast } from '../composables/useToast'
import { formatBytes } from '../lib/format'
import { storageErrorMessage } from '../lib/admin'
import type { StoragePolicyInfo } from '../lib/admin'
import type { UpdateStoragePolicyInput } from '../api/client'
import { useAdminStore } from '../stores/admin'

const admin = useAdminStore()
const toast = useToast()

const NAME_MAX = 64
const ROOT_HINT = '绝对路径，例如 D:\\rapig\\storage2 或 /mnt/disk2/rapig'

interface PolicyForm {
  name: string
  rootPath: string
  enabled: boolean
  isDefault: boolean
}

function emptyForm(): PolicyForm {
  return { name: '', rootPath: '', enabled: true, isDefault: false }
}

/* ------------------------------------------------------------- create */
const createOpen = ref(false)
const createForm = reactive<PolicyForm>(emptyForm())
const createBusy = ref(false)
const createNameError = ref<string | null>(null)
const createPathError = ref<string | null>(null)
const createError = ref<string | null>(null)
const nameRef = ref<HTMLInputElement | null>(null)

/* --------------------------------------------------------------- edit */
const editOpen = ref(false)
const editTarget = ref<StoragePolicyInfo | null>(null)
const editForm = reactive<PolicyForm>(emptyForm())
const editBusy = ref(false)
const editNameError = ref<string | null>(null)
const editPathError = ref<string | null>(null)
const editError = ref<string | null>(null)

/** A root path may only move while the policy holds no nodes and no files. */
const editPathLocked = computed(() => {
  const target = editTarget.value
  if (!target) return true
  return target.node_count + target.file_count > 0
})

/* ------------------------------------------------------------- delete */
const confirm = reactive<{ open: boolean; policy: StoragePolicyInfo | null; busy: boolean }>({
  open: false,
  policy: null,
  busy: false,
})
const confirmMessage = computed(() =>
  confirm.policy ? `确定要删除存储「${confirm.policy.name}」吗？` : '',
)

const toggleBusyId = ref<number | null>(null)

const totalUsedLabel = computed(() => {
  const sum = admin.storagePolicies.reduce((acc, policy) => acc + policy.used_bytes, 0)
  return formatBytes(sum)
})

function isAbsolutePath(value: string): boolean {
  return /^\//.test(value) || /^[A-Za-z]:[\\/]/.test(value) || /^\\\\[^\\]/.test(value)
}

function validateName(value: string): string | null {
  const text = value.trim()
  if (!text) return '请填写存储名称'
  if (Array.from(text).length > NAME_MAX) return `名称不能超过 ${NAME_MAX} 个字符`
  return null
}

function validateRootPath(value: string): string | null {
  const text = value.trim()
  if (!text) return '请填写根路径'
  if (!isAbsolutePath(text)) return '根路径必须是绝对路径'
  return null
}

function openCreate(): void {
  createForm.name = ''
  createForm.rootPath = ''
  createForm.enabled = true
  // The first policy has to be the default one.
  createForm.isDefault = admin.storagePolicies.length === 0
  createNameError.value = null
  createPathError.value = null
  createError.value = null
  createBusy.value = false
  createOpen.value = true
  queueMicrotask(() => nameRef.value?.focus())
}

function closeCreate(): void {
  if (createBusy.value) return
  createOpen.value = false
}

async function submitCreate(): Promise<void> {
  if (createBusy.value) return

  const name = createForm.name.trim()
  const rootPath = createForm.rootPath.trim()
  createNameError.value = validateName(name)
  createPathError.value = validateRootPath(rootPath)
  createError.value = null
  if (createNameError.value || createPathError.value) return

  createBusy.value = true
  try {
    await admin.createStorage({
      name,
      root_path: rootPath,
      enabled: createForm.enabled,
      is_default: createForm.isDefault,
    })
    createOpen.value = false
    toast.push(`已创建存储「${name}」`)
  } catch (cause) {
    createError.value = storageErrorMessage(cause, '创建存储失败，请稍后重试')
  } finally {
    createBusy.value = false
  }
}

function openEdit(policy: StoragePolicyInfo): void {
  editTarget.value = policy
  editForm.name = policy.name
  editForm.rootPath = policy.root_path
  editForm.enabled = policy.enabled
  editForm.isDefault = policy.is_default
  editNameError.value = null
  editPathError.value = null
  editError.value = null
  editBusy.value = false
  editOpen.value = true
}

function closeEdit(): void {
  if (editBusy.value) return
  editOpen.value = false
}

async function submitEdit(): Promise<void> {
  const target = editTarget.value
  if (!target || editBusy.value) return

  const name = editForm.name.trim()
  editNameError.value = validateName(name)
  editPathError.value = null
  editError.value = null
  if (!editPathLocked.value) editPathError.value = validateRootPath(editForm.rootPath)
  if (editNameError.value || editPathError.value) return

  const patch: UpdateStoragePolicyInput = {}
  if (name !== target.name) patch.name = name
  if (!editPathLocked.value && editForm.rootPath.trim() !== target.root_path) {
    patch.root_path = editForm.rootPath.trim()
  }
  if (editForm.enabled !== target.enabled) patch.enabled = editForm.enabled
  if (editForm.isDefault !== target.is_default) patch.is_default = editForm.isDefault

  if (Object.keys(patch).length === 0) {
    editOpen.value = false
    return
  }

  editBusy.value = true
  try {
    await admin.patchStorage(target.id, patch)
    editOpen.value = false
    toast.push(`已保存「${name}」`)
  } catch (cause) {
    editError.value = storageErrorMessage(cause, '保存失败，请稍后重试')
  } finally {
    editBusy.value = false
  }
}

async function toggleEnabled(policy: StoragePolicyInfo): Promise<void> {
  if (toggleBusyId.value !== null) return
  toggleBusyId.value = policy.id
  try {
    await admin.patchStorage(policy.id, { enabled: !policy.enabled })
    toast.push(policy.enabled ? `已停用「${policy.name}」` : `已启用「${policy.name}」`)
  } catch (cause) {
    toast.push(storageErrorMessage(cause, '操作失败，请稍后重试'), 'error')
  } finally {
    toggleBusyId.value = null
  }
}

function openDelete(policy: StoragePolicyInfo): void {
  confirm.policy = policy
  confirm.open = true
  confirm.busy = false
}

async function runDelete(): Promise<void> {
  const policy = confirm.policy
  if (!policy || confirm.busy) return

  confirm.busy = true
  try {
    await admin.removeStorage(policy.id)
    confirm.open = false
    toast.push(`已删除存储「${policy.name}」`)
  } catch (cause) {
    // 409 storage_in_use / default_policy / last_policy: surface the server's reason.
    confirm.open = false
    toast.push(storageErrorMessage(cause, '删除失败，请稍后重试'), 'error')
  } finally {
    confirm.busy = false
  }
}

function retry(): void {
  void admin.loadStorage()
}

onMounted(() => {
  void admin.loadStorage()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">存储</h1>
        <p class="page-sub">内容寻址对象的本地存储根</p>
      </div>
      <div class="page-actions">
        <button type="button" class="btn btn-primary" @click="openCreate">
          <svg width="15" height="15" viewBox="0 0 20 20" aria-hidden="true">
            <path d="M10 4.4v11.2M4.4 10h11.2" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
          </svg>
          新建存储
        </button>
      </div>
    </header>

    <section class="panel" aria-label="存储列表">
      <div class="table-scroll">
        <table class="table">
          <thead>
            <tr>
              <th scope="col">名称</th>
              <th scope="col">根路径</th>
              <th scope="col">状态</th>
              <th scope="col">默认</th>
              <th scope="col">已用</th>
              <th scope="col">节点 / 文件</th>
              <th scope="col" class="col-actions">操作</th>
            </tr>
          </thead>

          <tbody v-if="admin.storageLoading && admin.storagePolicies.length === 0">
            <tr v-for="row in 3" :key="row" class="skeleton-row">
              <td v-for="col in 7" :key="col"><span class="skeleton-block"></span></td>
            </tr>
          </tbody>

          <tbody v-else-if="admin.storageError">
            <tr>
              <td colspan="7">
                <div class="state" role="alert">
                  <p class="state-title">加载失败</p>
                  <p class="state-copy">{{ admin.storageError }}</p>
                  <button type="button" class="btn btn-secondary" @click="retry">重试</button>
                </div>
              </td>
            </tr>
          </tbody>

          <tbody v-else-if="admin.storagePolicies.length === 0">
            <tr>
              <td colspan="7">
                <div class="state">
                  <p class="state-title">还没有存储策略</p>
                  <p class="state-copy">先创建第一个存储根，之后在根目录新建文件夹时就可以选择存放位置。</p>
                  <button type="button" class="btn btn-primary" @click="openCreate">新建存储</button>
                </div>
              </td>
            </tr>
          </tbody>

          <tbody v-else>
            <tr v-for="policy in admin.storagePolicies" :key="policy.id">
              <td class="cell-name">{{ policy.name }}</td>
              <td class="cell-path">
                <code class="path">{{ policy.root_path }}</code>
              </td>
              <td>
                <span class="status-badge" :class="policy.enabled ? 'is-active' : 'is-disabled'">
                  <span class="dot" aria-hidden="true"></span>{{ policy.enabled ? '启用' : '停用' }}
                </span>
              </td>
              <td>
                <span v-if="policy.is_default" class="default-chip">默认</span>
                <span v-else class="cell-muted">—</span>
              </td>
              <td class="cell-muted numeric">{{ formatBytes(policy.used_bytes) }}</td>
              <td class="cell-muted numeric">{{ policy.node_count }} / {{ policy.file_count }}</td>
              <td class="col-actions">
                <div class="row-actions">
                  <button type="button" class="text-button" @click="openEdit(policy)">编辑</button>
                  <button
                    type="button"
                    class="text-button"
                    :class="{ 'is-danger': policy.enabled }"
                    :disabled="toggleBusyId !== null"
                    @click="toggleEnabled(policy)"
                  >
                    {{ policy.enabled ? '停用' : '启用' }}
                  </button>
                  <button
                    type="button"
                    class="text-button is-danger"
                    :disabled="toggleBusyId !== null"
                    :title="policy.is_default ? '默认存储需要先把其它存储设为默认' : ''"
                    @click="openDelete(policy)"
                  >
                    删除
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p class="panel-hint">
        共 {{ admin.storagePolicies.length }} 个存储，已用 {{ totalUsedLabel }}。删除仅在该存储下没有任何节点与文件时可用。
      </p>
    </section>

    <!-- create -->
    <Teleport to="body">
      <Transition name="dialog">
        <div v-if="createOpen" class="overlay" @mousedown.self="closeCreate">
          <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="create-storage-title">
            <h2 id="create-storage-title" class="dialog-title">新建存储</h2>
            <p class="dialog-sub">每个存储是一个独立的本地根目录，对象数据按策略分开存放。</p>
            <form class="dialog-form" novalidate @submit.prevent="submitCreate">
              <label class="field">
                <span class="field-label">名称</span>
                <input
                  id="storage-name"
                  ref="nameRef"
                  v-model="createForm.name"
                  class="input"
                  type="text"
                  maxlength="64"
                  autocomplete="off"
                  spellcheck="false"
                  placeholder="1–64 个字符"
                  :aria-invalid="createNameError ? 'true' : undefined"
                />
                <p v-if="createNameError" class="field-error" role="alert">{{ createNameError }}</p>
              </label>
              <label class="field">
                <span class="field-label">根路径</span>
                <input
                  id="storage-root"
                  v-model="createForm.rootPath"
                  class="input mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  placeholder="D:\rapig\storage2"
                  :aria-invalid="createPathError ? 'true' : undefined"
                />
                <p v-if="createPathError" class="field-error" role="alert">{{ createPathError }}</p>
                <span v-else class="field-hint">{{ ROOT_HINT }}</span>
              </label>
              <div class="switch-row">
                <span class="switch-copy">
                  <span class="switch-title">启用</span>
                  <span class="switch-hint">停用后不再向该存储写入新数据</span>
                </span>
                <label class="switch">
                  <input v-model="createForm.enabled" type="checkbox" role="switch" aria-label="启用" />
                  <span class="track"><span class="thumb"></span></span>
                </label>
              </div>
              <div class="switch-row">
                <span class="switch-copy">
                  <span class="switch-title">设为默认</span>
                  <span class="switch-hint">新建根文件夹时默认选中的存储</span>
                </span>
                <label class="switch">
                  <input v-model="createForm.isDefault" type="checkbox" role="switch" aria-label="设为默认" />
                  <span class="track"><span class="thumb"></span></span>
                </label>
              </div>
              <p class="dialog-note">保存时会检查路径存在、可写、不与其它策略重叠。</p>
              <p v-if="createError" class="field-error" role="alert">{{ createError }}</p>
              <div class="dialog-actions">
                <button type="button" class="btn btn-secondary" :disabled="createBusy" @click="closeCreate">
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
        <div v-if="editOpen" class="overlay" @mousedown.self="closeEdit">
          <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="edit-storage-title">
            <h2 id="edit-storage-title" class="dialog-title">编辑「{{ editTarget?.name }}」</h2>
            <p class="dialog-sub">名称随时可改；根路径只在存储还没有数据时才能调整。</p>
            <form class="dialog-form" novalidate @submit.prevent="submitEdit">
              <label class="field">
                <span class="field-label">名称</span>
                <input
                  id="storage-edit-name"
                  v-model="editForm.name"
                  class="input"
                  type="text"
                  maxlength="64"
                  autocomplete="off"
                  spellcheck="false"
                  :aria-invalid="editNameError ? 'true' : undefined"
                />
                <p v-if="editNameError" class="field-error" role="alert">{{ editNameError }}</p>
              </label>
              <label class="field">
                <span class="field-label">根路径</span>
                <input
                  id="storage-edit-root"
                  v-model="editForm.rootPath"
                  class="input mono"
                  :class="{ 'is-readonly': editPathLocked }"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  :readonly="editPathLocked"
                  :aria-invalid="editPathError ? 'true' : undefined"
                />
                <p v-if="editPathError" class="field-error" role="alert">{{ editPathError }}</p>
                <span v-else-if="editPathLocked" class="field-hint">该存储已有数据，不能修改根路径</span>
                <span v-else class="field-hint">{{ ROOT_HINT }}</span>
              </label>
              <div class="switch-row">
                <span class="switch-copy">
                  <span class="switch-title">启用</span>
                  <span class="switch-hint">停用后不再向该存储写入新数据</span>
                </span>
                <label class="switch">
                  <input v-model="editForm.enabled" type="checkbox" role="switch" aria-label="启用" />
                  <span class="track"><span class="thumb"></span></span>
                </label>
              </div>
              <div class="switch-row">
                <span class="switch-copy">
                  <span class="switch-title">设为默认</span>
                  <span class="switch-hint">
                    {{ editTarget?.is_default ? '当前已是默认存储' : '新建根文件夹时默认选中的存储' }}
                  </span>
                </span>
                <label class="switch" :class="{ 'is-disabled': editTarget?.is_default }">
                  <input
                    v-model="editForm.isDefault"
                    type="checkbox"
                    role="switch"
                    aria-label="设为默认"
                    :disabled="editTarget?.is_default"
                  />
                  <span class="track"><span class="thumb"></span></span>
                </label>
              </div>
              <p v-if="editError" class="field-error" role="alert">{{ editError }}</p>
              <div class="dialog-actions">
                <button type="button" class="btn btn-secondary" :disabled="editBusy" @click="closeEdit">
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

    <ConfirmDialog
      :open="confirm.open"
      title="删除存储"
      :message="confirmMessage"
      detail="仅当该存储下没有任何节点与文件时才能删除；默认存储和最后一个存储也不能删除。"
      confirm-label="删除"
      danger
      :busy="confirm.busy"
      @close="confirm.open = false"
      @confirm="runDelete"
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
  align-items: center;
  gap: 8px;
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
  min-width: 900px;
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
  font-weight: 500;
}

.cell-path {
  max-width: 340px;
}

.path {
  display: inline-block;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  font-size: var(--fs-xs);
  line-height: 1.5;
  color: var(--color-text);
  word-break: break-all;
  white-space: normal;
}

.status-badge,
.default-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 7px;
  border-radius: var(--radius-pill);
  font-size: var(--fs-xs);
  font-weight: 500;
  white-space: nowrap;
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

.default-chip {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
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

.text-button.is-danger:hover:not(:disabled) {
  background: var(--color-danger-soft);
}

.panel-hint {
  margin: 12px 2px 14px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
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

.mono {
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  font-size: var(--fs-xs);
}

.input.is-readonly {
  background: var(--color-bg);
  color: var(--color-muted);
}

.switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 8px 0;
  border-top: 1px solid var(--color-border);
}

.switch-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.switch-title {
  font-size: var(--fs-sm);
  font-weight: 500;
}

.switch-hint {
  color: var(--color-muted);
  font-size: var(--fs-xs);
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

.dialog-note {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--fs-xs);
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
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
}
</style>
