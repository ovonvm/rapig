import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getPublicShare, listPublicNodes, publicDownloadUrl, unlockShare } from '../api/client'
import type { PublicNode, PublicShareMeta } from '../api/client'
import { errorCodeOf, messageForPublicShare, messageForSharePassword } from '../lib/errors'

const PAGE_SIZE = 50

/**
 * Anonymous `/s/:token` page: fetch metadata, unlock with a password when the
 * link needs one, then browse a directory share or expose its file download.
 * Nothing here leaks owner information — the page only shows the contract's
 * public fields.
 */
export const usePublicShareStore = defineStore('publicShare', () => {
  const token = ref('')
  const meta = ref<PublicShareMeta | null>(null)
  const loading = ref(false)
  const errorCode = ref<string | null>(null)
  const errorMessage = ref<string | null>(null)

  const unlocking = ref(false)
  const unlockError = ref<string | null>(null)

  const entries = ref<PublicNode[]>([])
  const entriesTotal = ref(0)
  const entriesLoading = ref(false)
  const entriesError = ref<string | null>(null)
  const currentParent = ref<number | null>(null)

  const locked = computed(() => meta.value?.locked === true)
  const dead = computed(() => errorCode.value === 'not_found' || errorCode.value === 'share_exhausted')
  const kind = computed(() => meta.value?.kind ?? 'file')
  /** Merged (multi-node) shares behave like a listing whose root is the member set. */
  const listed = computed(() => kind.value === 'dir' || kind.value === 'multi')
  const name = computed(() => meta.value?.name ?? '')
  const fileDownloadUrl = computed(() =>
    token.value ? publicDownloadUrl(token.value, meta.value?.node_id ?? 0) : '',
  )

  function resetTransient(): void {
    meta.value = null
    errorCode.value = null
    errorMessage.value = null
    unlockError.value = null
    unlocking.value = false
    entries.value = []
    entriesTotal.value = 0
    entriesError.value = null
    currentParent.value = null
  }

  async function readMeta(): Promise<void> {
    const share = await getPublicShare(token.value)
    meta.value = share
  }

  /** First load / retry for the whole page. */
  async function open(nextToken: string): Promise<void> {
    token.value = nextToken
    resetTransient()
    loading.value = true

    try {
      await readMeta()
      if (!locked.value && listed.value) await loadNodes(null, 0)
    } catch (cause) {
      meta.value = null
      errorCode.value = errorCodeOf(cause)
      errorMessage.value = messageForPublicShare(cause)
    } finally {
      loading.value = false
    }
  }

  /** Exchange the password for the ticket cookie, then re-read the metadata. */
  async function unlock(password: string): Promise<boolean> {
    if (unlocking.value) return false

    unlocking.value = true
    unlockError.value = null

    try {
      await unlockShare(token.value, password)
      await readMeta()
      if (listed.value) await loadNodes(null, 0)
      return true
    } catch (cause) {
      unlockError.value = messageForSharePassword(cause)
      return false
    } finally {
      unlocking.value = false
    }
  }

  /** Directory share listing; `parent === null` means the share root. */
  async function loadNodes(parent: number | null, offset = 0): Promise<void> {
    entriesLoading.value = true
    entriesError.value = null

    try {
      const page = await listPublicNodes(token.value, {
        parent: parent === null ? undefined : parent,
        limit: PAGE_SIZE,
        offset,
      })
      const seen = new Set(offset === 0 ? [] : entries.value.map((entry) => entry.id))
      entries.value = offset === 0 ? page.items : [...entries.value, ...page.items.filter((entry) => !seen.has(entry.id))]
      entriesTotal.value = page.total
      currentParent.value = parent
    } catch (cause) {
      entriesError.value = messageForPublicShare(cause)
    } finally {
      entriesLoading.value = false
    }
  }

  async function loadMoreNodes(): Promise<void> {
    if (entriesLoading.value || entries.value.length >= entriesTotal.value) return
    await loadNodes(currentParent.value, entries.value.length)
  }

  function reset(): void {
    token.value = ''
    loading.value = false
    resetTransient()
  }

  return {
    token,
    meta,
    loading,
    errorCode,
    errorMessage,
    unlocking,
    unlockError,
    entries,
    entriesTotal,
    entriesLoading,
    entriesError,
    currentParent,
    locked,
    dead,
    kind,
    name,
    fileDownloadUrl,
    open,
    unlock,
    loadNodes,
    loadMoreNodes,
    reset,
  }
})
