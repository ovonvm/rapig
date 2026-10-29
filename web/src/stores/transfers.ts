import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  createTransfer,
  createTransferTorrent,
  deleteTransfer,
  getQBStatus,
  listTransfers,
  pauseTransfer,
  resumeTransfer,
} from '../api/client'
import type { CreateTransferInput, TransferItem, TransferState } from '../api/client'
import { messageForError } from '../lib/errors'

const PAGE_SIZE = 50
/** The server downloads in the background; 2s is enough to feel live. */
export const TRANSFER_POLL_MS = 2000

export type TransferFilter = 'all' | 'active' | 'completed' | 'failed'

const ACTIVE_STATES: readonly TransferState[] = ['pending', 'running', 'paused']

export function isActiveTransfer(state: TransferState): boolean {
  return ACTIVE_STATES.includes(state)
}

function matchesFilter(item: TransferItem, filter: TransferFilter): boolean {
  if (filter === 'active') return isActiveTransfer(item.state)
  if (filter === 'completed') return item.state === 'completed'
  if (filter === 'failed') return item.state === 'failed' || item.state === 'canceled'
  return true
}

/** 「离线下载」: server-side URL fetches with a 2s poll while work is pending. */
export const useTransfersStore = defineStore('transfers', () => {
  const items = ref<TransferItem[]>([])
  const total = ref(0)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref<string | null>(null)
  const loaded = ref(false)
  const filter = ref<TransferFilter>('all')
  /** Magnet entry visibility; false until the server says qBittorrent is wired. */
  const qbEnabled = ref(false)
  const qbStatusKnown = ref(false)

  let requestId = 0
  /** Set by the view while it is mounted, so no timer survives navigation. */
  let wantsPolling = false
  let timer: number | null = null

  const visible = computed(() => items.value.filter((item) => matchesFilter(item, filter.value)))
  const hasActive = computed(() => items.value.some((item) => isActiveTransfer(item.state)))
  const counts = computed(() => ({
    all: items.value.length,
    active: items.value.filter((item) => isActiveTransfer(item.state)).length,
    completed: items.value.filter((item) => item.state === 'completed').length,
    failed: items.value.filter((item) => item.state === 'failed' || item.state === 'canceled').length,
  }))
  const hasMore = computed(() => items.value.length < total.value)
  const polling = computed(() => timer !== null)

  function clearTimer(): void {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  function schedule(): void {
    if (!wantsPolling || !hasActive.value) {
      clearTimer()
      return
    }
    if (timer !== null) return
    timer = window.setTimeout(async () => {
      timer = null
      await load({ silent: true })
      // `load` calls schedule() again while the list still has live tasks.
    }, TRANSFER_POLL_MS)
  }

  async function load({ silent = false }: { silent?: boolean } = {}): Promise<void> {
    const current = ++requestId
    if (!silent) loading.value = true
    if (!silent) error.value = null

    try {
      const page = await listTransfers({ limit: PAGE_SIZE, offset: 0 })
      if (current !== requestId) return
      items.value = page.items
      total.value = page.total
      loaded.value = true
      if (!silent) error.value = null
    } catch (cause) {
      if (current !== requestId) return
      if (!silent || items.value.length === 0) {
        error.value = messageForError(cause, '离线下载列表加载失败，请稍后重试')
      }
    } finally {
      if (current === requestId) {
        loading.value = false
        schedule()
      }
    }
  }

  async function loadMore(): Promise<void> {
    if (loading.value || loadingMore.value || !hasMore.value) return
    loadingMore.value = true
    try {
      const page = await listTransfers({ limit: PAGE_SIZE, offset: items.value.length })
      const seen = new Set(items.value.map((item) => item.id))
      items.value = [...items.value, ...page.items.filter((item) => !seen.has(item.id))]
      total.value = page.total
    } catch (cause) {
      error.value = messageForError(cause, '加载更多失败，请稍后重试')
    } finally {
      loadingMore.value = false
      schedule()
    }
  }

  /** Client-side tab filter; the API returns every state in one page. */
  function setFilter(next: TransferFilter): void {
    filter.value = next
  }

  /**
   * Magnet support is advisory: when the status call fails the entry stays
   * hidden instead of turning the whole page into an error.
   */
  async function loadQBStatus({ force = false }: { force?: boolean } = {}): Promise<boolean> {
    if (qbStatusKnown.value && !force) return qbEnabled.value
    try {
      qbEnabled.value = await getQBStatus()
    } catch {
      qbEnabled.value = false
    } finally {
      qbStatusKnown.value = true
    }
    return qbEnabled.value
  }

  async function create(input: CreateTransferInput): Promise<TransferItem> {
    const transfer = await createTransfer(input)
    items.value = [transfer, ...items.value.filter((item) => item.id !== transfer.id)]
    total.value += 1
    loaded.value = true
    schedule()
    return transfer
  }

  /** Upload a `.torrent` file; the server hands it to qBittorrent. */
  async function createTorrent(
    file: File,
    input: { parent?: number; name?: string } = {},
  ): Promise<TransferItem> {
    const transfer = await createTransferTorrent(file, input)
    items.value = [transfer, ...items.value.filter((item) => item.id !== transfer.id)]
    total.value += 1
    loaded.value = true
    schedule()
    return transfer
  }

  async function pause(id: string): Promise<void> {
    await pauseTransfer(id)
    const target = items.value.find((item) => item.id === id)
    if (target) target.state = 'paused'
    schedule()
  }

  async function resume(id: string): Promise<void> {
    await resumeTransfer(id)
    const target = items.value.find((item) => item.id === id)
    if (target) target.state = 'pending'
    schedule()
  }

  async function remove(id: string): Promise<void> {
    await deleteTransfer(id)
    const before = items.value.length
    items.value = items.value.filter((item) => item.id !== id)
    if (items.value.length < before) total.value = Math.max(0, total.value - 1)
    schedule()
  }

  /** The view owns polling lifetime: start on mount, stop on unmount. */
  function startPolling(): void {
    wantsPolling = true
    schedule()
  }

  function stopPolling(): void {
    wantsPolling = false
    clearTimer()
  }

  function reset(): void {
    stopPolling()
    requestId += 1
    items.value = []
    total.value = 0
    loading.value = false
    loadingMore.value = false
    error.value = null
    loaded.value = false
    filter.value = 'all'
    qbEnabled.value = false
    qbStatusKnown.value = false
  }

  return {
    items,
    total,
    loading,
    loadingMore,
    error,
    loaded,
    filter,
    qbEnabled,
    qbStatusKnown,
    visible,
    hasActive,
    counts,
    hasMore,
    polling,
    load,
    loadMore,
    setFilter,
    loadQBStatus,
    create,
    createTorrent,
    pause,
    resume,
    remove,
    startPolling,
    stopPolling,
    reset,
  }
})
