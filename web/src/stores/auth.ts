import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { fetchMe, login as apiLogin, logout as apiLogout } from '../api/client'
import type { User } from '../api/client'
import { useMediaStore } from './media'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const hydrated = ref(false)

  let sessionRequest: Promise<void> | null = null

  const isAuthenticated = computed(() => user.value !== null)
  const isAdmin = computed(() => user.value?.role === 'admin')
  /** Root administrator: the only one allowed to change the registration switch. */
  const isRoot = computed(() => user.value?.is_root === true)
  /** Delegated group creation/admin rights (P1). */
  const canCreateGroups = computed(() => user.value?.can_create_groups === true)
  /** Admin or delegate: may open the group console (`/admin/groups`). */
  const canManageGroups = computed(() => isAdmin.value || canCreateGroups.value)

  /** Restore the session once per page load; 401 simply means "signed out". */
  function ensureSession(): Promise<void> {
    if (hydrated.value) return Promise.resolve()
    if (sessionRequest) return sessionRequest

    sessionRequest = (async () => {
      try {
        user.value = await fetchMe()
      } catch {
        user.value = null
      } finally {
        hydrated.value = true
        sessionRequest = null
      }
    })()

    return sessionRequest
  }

  async function login(username: string, password: string): Promise<void> {
    // Never let the previous account's audio survive a new login (stale tab or
    // expired session -> login as someone else).
    useMediaStore().stop()
    user.value = await apiLogin(username, password)
    hydrated.value = true
  }

  /** Re-read the profile so `used_bytes`/`quota_bytes` follow uploads. */
  async function refreshUser(): Promise<void> {
    if (!user.value) return
    try {
      user.value = await fetchMe()
    } catch {
      // A transient failure must not sign the user out on its own.
    }
  }

  async function logout(): Promise<void> {
    try {
      await apiLogout()
    } finally {
      useMediaStore().stop()
      user.value = null
      hydrated.value = true
    }
  }

  function clearUser(): void {
    user.value = null
  }

  return {
    user,
    hydrated,
    isAuthenticated,
    isAdmin,
    isRoot,
    canCreateGroups,
    canManageGroups,
    ensureSession,
    login,
    refreshUser,
    logout,
    clearUser,
  }
})
