import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  createShare,
  listShares,
  purgeShare,
  restoreShare,
  revokeShare,
} from '../api/client'
import type { CreateShareInput, ShareInfo, ShareStatusFilter } from '../api/client'
import { messageForError } from '../lib/errors'

const PAGE_SIZE = 50

/** Owner view of 「我的分享」: status filter, paged list, lifecycle actions. */
export const useSharesStore = defineStore('shares', () => {
  const items = ref<ShareInfo[]>([])
  const total = ref(0)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref<string | null>(null)
  const loaded = ref(false)
  const status = ref<ShareStatusFilter>('all')

  let requestId = 0

  async function load(): Promise<void> {
    const current = ++requestId
    loading.value = true
    error.value = null

    try {
      const page = await listShares({ status: status.value, limit: PAGE_SIZE, offset: 0 })
      if (current !== requestId) return
      items.value = page.items
      total.value = page.total
      loaded.value = true
    } catch (cause) {
      if (current !== requestId) return
      items.value = []
      total.value = 0
      error.value = messageForError(cause, '分享列表加载失败，请稍后重试')
    } finally {
      if (current === requestId) loading.value = false
    }
  }

  /** Switch the 全部/有效/已撤销/已过期 filter and refetch from offset 0. */
  async function setStatus(next: ShareStatusFilter): Promise<void> {
    if (status.value === next) return
    status.value = next
    await load()
  }

  async function loadMore(): Promise<void> {
    if (loading.value || loadingMore.value || items.value.length >= total.value) return

    loadingMore.value = true
    try {
      const page = await listShares({ status: status.value, limit: PAGE_SIZE, offset: items.value.length })
      const seen = new Set(items.value.map((item) => item.id))
      items.value = [...items.value, ...page.items.filter((item) => !seen.has(item.id))]
      total.value = page.total
    } catch (cause) {
      error.value = messageForError(cause, '加载更多失败，请稍后重试')
    } finally {
      loadingMore.value = false
    }
  }

  /** Create a link and put it at the top of the list. */
  async function create(input: CreateShareInput): Promise<ShareInfo> {
    const share = await createShare(input)
    items.value = [share, ...items.value.filter((item) => item.id !== share.id)]
    total.value += 1
    return share
  }

  /** Revoke keeps the record visible with the 已撤销 state. */
  async function revoke(id: string): Promise<void> {
    await revokeShare(id)
    const target = items.value.find((item) => item.id === id)
    if (target) target.active = false
  }

  /** Restore a revoked link in place (same token, counters untouched). */
  async function restore(id: string): Promise<void> {
    const share = await restoreShare(id)
    const index = items.value.findIndex((item) => item.id === id)
    if (index >= 0) items.value[index] = share
  }

  /** Permanently remove a revoked/expired record. */
  async function purge(id: string): Promise<void> {
    await purgeShare(id)
    const before = items.value.length
    items.value = items.value.filter((item) => item.id !== id)
    if (items.value.length < before) total.value = Math.max(0, total.value - 1)
  }

  function reset(): void {
    requestId += 1
    items.value = []
    total.value = 0
    loading.value = false
    loadingMore.value = false
    error.value = null
    loaded.value = false
    status.value = 'all'
  }

  return {
    items,
    total,
    loading,
    loadingMore,
    error,
    loaded,
    status,
    load,
    setStatus,
    loadMore,
    create,
    revoke,
    restore,
    purge,
    reset,
  }
})
