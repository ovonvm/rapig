import { ref } from 'vue'
import { defineStore } from 'pinia'
import { ApiError, deleteNode, emptyTrash, listTrash, restoreNode } from '../api/client'
import type { NodeItem, TrashItem } from '../api/client'
import { useAuthStore } from './auth'
import { useFilesStore } from './files'

const PAGE_LIMIT = 50

/**
 * Recycle bin. Deleting a node only marks it trashed, so the listing here is
 * the only place a restore/purge can start from.
 */
export const useTrashStore = defineStore('trash', () => {
  const items = ref<TrashItem[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const errorCode = ref<string | null>(null)
  const loaded = ref(false)

  let latestRequest = 0

  async function load(): Promise<void> {
    const requestId = ++latestRequest
    loading.value = true
    error.value = null
    errorCode.value = null

    try {
      const page = await listTrash({ limit: PAGE_LIMIT, offset: 0 })
      if (requestId !== latestRequest) return
      items.value = page.items
      total.value = page.total
      loaded.value = true
    } catch (cause) {
      if (requestId !== latestRequest) return
      error.value = cause instanceof ApiError ? cause.message : '加载失败，请稍后重试'
      errorCode.value = cause instanceof ApiError ? cause.code : null
    } finally {
      if (requestId === latestRequest) loading.value = false
    }
  }

  /** Bring one entry back; the returned node may carry a de-conflicted name. */
  async function restore(id: number): Promise<NodeItem> {
    const node = await restoreNode(id)
    items.value = items.value.filter((item) => item.id !== id)
    total.value = Math.max(0, total.value - 1)
    // A restored node can reappear in the drive root, so refresh that listing.
    await useFilesStore().load()
    return node
  }

  /** Permanently delete one entry; this is the only step that frees quota. */
  async function purge(id: number): Promise<void> {
    await deleteNode(id, true)
    items.value = items.value.filter((item) => item.id !== id)
    total.value = Math.max(0, total.value - 1)
    await useAuthStore().refreshUser()
  }

  /** Permanently delete everything; resolves with the purged count. */
  async function empty(): Promise<number> {
    const purged = await emptyTrash()
    items.value = []
    total.value = 0
    await useAuthStore().refreshUser()
    return purged
  }

  /**
   * Cheap count refresh for the sidebar badge: one-entry page still reports the
   * full total, so this does not download the whole list again.
   */
  async function refreshTotal(): Promise<void> {
    try {
      const page = await listTrash({ limit: 1, offset: 0 })
      total.value = page.total
    } catch {
      // The badge is advisory: keep the previous number on failure.
    }
  }

  function reset(): void {
    items.value = []
    total.value = 0
    loading.value = false
    error.value = null
    errorCode.value = null
    loaded.value = false
  }

  return { items, total, loading, error, errorCode, loaded, load, refreshTotal, restore, purge, empty, reset }
})
