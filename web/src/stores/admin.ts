import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  createAdminStorage,
  createAdminUser,
  deleteAdminStorage,
  getAdminSettings,
  listAdminAudit,
  listAdminAuditActions,
  listAdminStorage,
  listAdminUsers,
  purgeAdminUser,
  resetAdminUserPassword,
  restoreAdminUser,
  updateAdminSettings,
  updateAdminStorage,
  updateAdminUser,
} from '../api/client'
import type {
  AdminAuditEntry,
  AdminSettings,
  AdminUser,
  AdminSettingsPatch,
  CreateAdminUserInput,
  CreateStoragePolicyInput,
  UpdateAdminUserInput,
  UpdateStoragePolicyInput,
} from '../api/client'
import { adminErrorMessage, storageErrorMessage } from '../lib/admin'
import type { StoragePolicyInfo } from '../lib/admin'

export const ADMIN_PAGE_SIZE = 50

export interface AuditFilters {
  actor: string
  action: string
  /** Unix seconds; null = unbounded. */
  from: number | null
  to: number | null
}

function emptyFilters(): AuditFilters {
  return { actor: '', action: '', from: null, to: null }
}

export const useAdminStore = defineStore('admin', () => {
  /* ------------------------------------------------------------ users */
  const users = ref<AdminUser[]>([])
  const usersTotal = ref(0)
  const usersLoading = ref(false)
  const usersError = ref<string | null>(null)
  const usersQuery = ref('')
  const usersOffset = ref(0)
  const hasMoreUsers = computed(() => users.value.length < usersTotal.value)
  let usersSeq = 0

  async function fetchUsers(q: string, offset: number, append: boolean): Promise<void> {
    const seq = ++usersSeq
    if (!append) {
      usersLoading.value = true
      usersError.value = null
    }
    try {
      const page = await listAdminUsers({ q: q.trim(), limit: ADMIN_PAGE_SIZE, offset })
      if (seq !== usersSeq) return
      usersQuery.value = q
      usersOffset.value = offset
      usersTotal.value = page.total
      if (!append) {
        users.value = page.items
        return
      }
      const seen = new Set(users.value.map((item) => item.id))
      users.value = [...users.value, ...page.items.filter((item) => !seen.has(item.id))]
    } catch (cause) {
      if (seq !== usersSeq) return
      if (append) throw cause
      usersError.value = adminErrorMessage(cause, '用户列表加载失败，请稍后重试')
    } finally {
      if (seq === usersSeq) usersLoading.value = false
    }
  }

  function loadUsers(q = usersQuery.value): Promise<void> {
    return fetchUsers(q, 0, false)
  }

  function loadMoreUsers(): Promise<void> {
    return fetchUsers(usersQuery.value, usersOffset.value + ADMIN_PAGE_SIZE, true)
  }

  async function createUser(input: CreateAdminUserInput): Promise<string | null> {
    const created = await createAdminUser(input)
    await loadUsers()
    return created.initial_password ?? null
  }

  async function patchUser(id: number, input: UpdateAdminUserInput): Promise<AdminUser> {
    const updated = await updateAdminUser(id, input)
    users.value = users.value.map((item) => (item.id === updated.id ? updated : item))
    return updated
  }

  async function resetPassword(id: number): Promise<string> {
    return resetAdminUserPassword(id)
  }

  /** Lifts the deletion grace period and patches the row in place (M5.2c). */
  async function restoreUser(id: number): Promise<AdminUser> {
    const restored = await restoreAdminUser(id)
    users.value = users.value.map((item) => (item.id === restored.id ? restored : item))
    return restored
  }

  /** Irreversibly deletes the account and its files, then drops the row. */
  async function purgeUser(id: number): Promise<void> {
    await purgeAdminUser(id)
    users.value = users.value.filter((item) => item.id !== id)
    usersTotal.value = Math.max(0, usersTotal.value - 1)
  }

  /* ------------------------------------------------------------ audit */
  const auditEntries = ref<AdminAuditEntry[]>([])
  const auditTotal = ref(0)
  const auditLoading = ref(false)
  const auditError = ref<string | null>(null)
  const auditFilters = ref<AuditFilters>(emptyFilters())
  const auditActions = ref<string[]>([])
  const auditActionsLoaded = ref(false)
  const auditOffset = ref(0)
  const hasMoreAudit = computed(() => auditEntries.value.length < auditTotal.value)
  let auditSeq = 0

  async function fetchAudit(filters: AuditFilters, offset: number, append: boolean): Promise<void> {
    const seq = ++auditSeq
    if (!append) {
      auditLoading.value = true
      auditError.value = null
    }
    try {
      const page = await listAdminAudit({
        actor: filters.actor.trim(),
        action: filters.action,
        from: filters.from ?? undefined,
        to: filters.to ?? undefined,
        limit: ADMIN_PAGE_SIZE,
        offset,
      })
      if (seq !== auditSeq) return
      auditFilters.value = filters
      auditOffset.value = offset
      auditTotal.value = page.total
      if (!append) {
        auditEntries.value = page.items
        return
      }
      const seen = new Set(auditEntries.value.map((item) => `${item.at}:${item.request_id}:${item.action}`))
      auditEntries.value = [
        ...auditEntries.value,
        ...page.items.filter((item) => !seen.has(`${item.at}:${item.request_id}:${item.action}`)),
      ]
    } catch (cause) {
      if (seq !== auditSeq) return
      if (append) throw cause
      auditError.value = adminErrorMessage(cause, '审计日志加载失败，请稍后重试')
    } finally {
      if (seq === auditSeq) auditLoading.value = false
    }
  }

  function loadAudit(filters = auditFilters.value): Promise<void> {
    return fetchAudit(filters, 0, false)
  }

  function loadMoreAudit(): Promise<void> {
    return fetchAudit(auditFilters.value, auditOffset.value + ADMIN_PAGE_SIZE, true)
  }

  async function loadAuditActions(): Promise<void> {
    if (auditActionsLoaded.value) return
    try {
      auditActions.value = await listAdminAuditActions()
      auditActionsLoaded.value = true
    } catch {
      /* the select simply stays empty; filtering by action is optional */
    }
  }

  /* --------------------------------------------------------- settings */
  const settings = ref<AdminSettings | null>(null)
  const settingsLoading = ref(false)
  const settingsError = ref<string | null>(null)
  const settingsSaving = ref(false)

  async function loadSettings(): Promise<void> {
    settingsLoading.value = true
    settingsError.value = null
    try {
      settings.value = await getAdminSettings()
    } catch (cause) {
      settingsError.value = adminErrorMessage(cause, '设置加载失败，请稍后重试')
    } finally {
      settingsLoading.value = false
    }
  }

  async function saveSettings(patch: AdminSettingsPatch): Promise<boolean> {
    settingsSaving.value = true
    try {
      settings.value = await updateAdminSettings(patch)
      return true
    } finally {
      settingsSaving.value = false
    }
  }

  /* ---------------------------------------------------------- storage */
  const storagePolicies = ref<StoragePolicyInfo[]>([])
  const storageLoading = ref(false)
  const storageError = ref<string | null>(null)
  let storageSeq = 0

  async function loadStorage(): Promise<void> {
    const seq = ++storageSeq
    storageLoading.value = true
    storageError.value = null
    try {
      const items = await listAdminStorage()
      if (seq !== storageSeq) return
      storagePolicies.value = items
    } catch (cause) {
      if (seq !== storageSeq) return
      storageError.value = storageErrorMessage(cause, '存储列表加载失败，请稍后重试')
    } finally {
      if (seq === storageSeq) storageLoading.value = false
    }
  }

  /** The create response is authoritative; a reload keeps counts/used bytes fresh. */
  async function createStorage(input: CreateStoragePolicyInput): Promise<StoragePolicyInfo> {
    const created = await createAdminStorage(input)
    await loadStorage()
    return created
  }

  async function patchStorage(id: number, input: UpdateStoragePolicyInput): Promise<StoragePolicyInfo> {
    const updated = await updateAdminStorage(id, input)
    storagePolicies.value = storagePolicies.value.map((item) => (item.id === updated.id ? updated : item))
    return updated
  }

  async function removeStorage(id: number): Promise<void> {
    await deleteAdminStorage(id)
    storagePolicies.value = storagePolicies.value.filter((item) => item.id !== id)
  }

  function resetAll(): void {
    usersSeq += 1
    auditSeq += 1
    storageSeq += 1
    users.value = []
    usersTotal.value = 0
    usersQuery.value = ''
    usersOffset.value = 0
    usersError.value = null
    auditEntries.value = []
    auditTotal.value = 0
    auditOffset.value = 0
    auditFilters.value = emptyFilters()
    auditError.value = null
    settings.value = null
    settingsError.value = null
    storagePolicies.value = []
    storageError.value = null
  }

  return {
    users,
    usersTotal,
    usersLoading,
    usersError,
    usersQuery,
    usersOffset,
    hasMoreUsers,
    loadUsers,
    loadMoreUsers,
    createUser,
    patchUser,
    resetPassword,
    restoreUser,
    purgeUser,
    auditEntries,
    auditTotal,
    auditLoading,
    auditError,
    auditFilters,
    auditActions,
    hasMoreAudit,
    loadAudit,
    loadMoreAudit,
    loadAuditActions,
    settings,
    settingsLoading,
    settingsError,
    settingsSaving,
    loadSettings,
    saveSettings,
    storagePolicies,
    storageLoading,
    storageError,
    loadStorage,
    createStorage,
    patchStorage,
    removeStorage,
    resetAll,
  }
})
