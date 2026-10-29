import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { searchNodes } from '../api/client'
import type { NodeItem, SearchType } from '../api/client'
import { errorCodeOf, messageForError } from '../lib/errors'

/** Rows per page; the API caps `limit` at 200 (default 50). */
export const SEARCH_PAGE_SIZE = 50

/** The server rejects longer queries with 400 `out_of_range`. */
export const SEARCH_QUERY_MAX = 100

export type SearchSort = 'relevance' | 'recent'

/** Trim + cap, so the UI never sends a request the server would reject. */
export function normalizeSearchQuery(raw: unknown): string {
  const text = typeof raw === 'string' ? raw : ''
  return text.trim().slice(0, SEARCH_QUERY_MAX)
}

export function normalizeSearchType(raw: unknown): SearchType {
  return raw === 'file' || raw === 'dir' ? raw : ''
}

export function normalizeSearchSort(raw: unknown): SearchSort {
  // The URL spells the flag as `recent=1`; accept the long form too so
  // hand-written links keep working.
  return raw === '1' || raw === 'true' || raw === 'recent' ? 'recent' : 'relevance'
}

/** `page` in the URL counts loaded pages (1-based) for refresh-restore. */
export function normalizeSearchPage(raw: unknown): number {
  const value =
    typeof raw === 'string' ? Number.parseInt(raw, 10) : typeof raw === 'number' ? raw : Number.NaN
  return Number.isFinite(value) && value >= 1 ? Math.floor(value) : 1
}

/**
 * One search result set. Requests are serialised with an AbortController plus a
 * sequence number: an older response can never overwrite a newer query, even
 * when the abort lands after the response was already read.
 */
export const useSearchStore = defineStore('search', () => {
  const q = ref('')
  const type = ref<SearchType>('')
  const sort = ref<SearchSort>('relevance')
  const items = ref<NodeItem[]>([])
  const total = ref(0)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref<string | null>(null)
  const errorCode = ref<string | null>(null)
  const moreError = ref<string | null>(null)
  /** Pages fetched so far; 0 means "no search has run". */
  const pages = ref(0)

  let controller: AbortController | null = null
  let sequence = 0

  const hasMore = computed(() => items.value.length < total.value)
  const searched = computed(() => pages.value > 0)
  const isEmpty = computed(
    () => searched.value && total.value === 0 && !loading.value && !error.value,
  )

  function abort(): void {
    controller?.abort()
    controller = null
  }

  function reset(): void {
    abort()
    sequence += 1
    q.value = ''
    type.value = ''
    sort.value = 'relevance'
    items.value = []
    total.value = 0
    loading.value = false
    loadingMore.value = false
    error.value = null
    errorCode.value = null
    moreError.value = null
    pages.value = 0
  }

  function fetchPage(offset: number, signal: AbortSignal): Promise<{ items: NodeItem[]; total: number }> {
    return searchNodes({
      q: q.value,
      type: type.value,
      recent: sort.value === 'recent',
      limit: SEARCH_PAGE_SIZE,
      offset,
      signal,
    })
  }

  /** First page for a query/filter combination; empty query clears results. */
  async function search(params: { q: string; type: SearchType; sort: SearchSort }): Promise<void> {
    const query = normalizeSearchQuery(params.q)
    abort()
    sequence += 1
    const current = sequence

    q.value = query
    type.value = params.type
    sort.value = params.sort
    items.value = []
    total.value = 0
    pages.value = 0
    error.value = null
    errorCode.value = null
    moreError.value = null

    if (!query) {
      loading.value = false
      return
    }

    loading.value = true
    controller = new AbortController()
    try {
      const page = await fetchPage(0, controller.signal)
      if (current !== sequence) return
      items.value = page.items
      total.value = page.total
      pages.value = 1
    } catch (cause) {
      if (current !== sequence) return
      if (errorCodeOf(cause) === 'aborted') return
      errorCode.value = errorCodeOf(cause)
      error.value = messageForError(cause, '搜索失败，请稍后重试')
    } finally {
      if (current === sequence) loading.value = false
    }
  }

  /** Appends the next page; failures keep the rows already on screen. */
  async function loadMore(): Promise<void> {
    if (loading.value || loadingMore.value || !q.value || !hasMore.value) return

    loadingMore.value = true
    moreError.value = null
    const current = sequence
    controller = new AbortController()
    try {
      const page = await fetchPage(items.value.length, controller.signal)
      if (current !== sequence) return
      const seen = new Set(items.value.map((item) => item.id))
      items.value = [...items.value, ...page.items.filter((item) => !seen.has(item.id))]
      total.value = page.total
      pages.value += 1
    } catch (cause) {
      if (current !== sequence) return
      if (errorCodeOf(cause) === 'aborted') return
      moreError.value = messageForError(cause, '加载更多失败，请稍后重试')
    } finally {
      if (current === sequence) loadingMore.value = false
    }
  }

  return {
    q,
    type,
    sort,
    items,
    total,
    loading,
    loadingMore,
    error,
    errorCode,
    moreError,
    pages,
    hasMore,
    searched,
    isEmpty,
    search,
    loadMore,
    reset,
  }
})
