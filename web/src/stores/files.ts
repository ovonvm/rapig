import { ref } from 'vue'
import { defineStore } from 'pinia'
import { ApiError, createNode, deleteNode, listNodes, updateNode } from '../api/client'
import type { CreateNodeInput, NodeItem } from '../api/client'

const PAGE_LIMIT = 50

export const useFilesStore = defineStore('files', () => {
  const items = ref<NodeItem[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const errorCode = ref<string | null>(null)
  const loaded = ref(false)
  /** Directory currently listed: 0 = drive root, otherwise a folder node id. */
  const parent = ref(0)

  let latestRequest = 0

  /**
   * List `parent` (0 = root). Omitting the argument re-lists the directory the
   * store already points at, so refreshes after uploads/moves stay in place.
   */
  async function load(nextParent?: number): Promise<void> {
    if (typeof nextParent === 'number' && Number.isInteger(nextParent) && nextParent >= 0) {
      parent.value = nextParent
    }

    const requestId = ++latestRequest
    loading.value = true
    error.value = null
    errorCode.value = null

    try {
      const page = await listNodes({ parent: parent.value, limit: PAGE_LIMIT, offset: 0 })
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

  /**
   * Create a folder inside the directory currently listed, then refresh it.
   * `storagePolicyId` only applies at the drive root (M5.3): a subfolder always
   * inherits the storage of its parent.
   */
  async function createFolder(name: string, storagePolicyId?: number | null): Promise<NodeItem> {
    const input: CreateNodeInput = { parent: parent.value, name, kind: 'dir' }
    if (parent.value === 0 && typeof storagePolicyId === 'number') input.storage_policy_id = storagePolicyId
    const node = await createNode(input)
    await load()
    return node
  }

  /** Replace one visible row with the server's fresh copy (name/size/revision). */
  function replaceNode(node: NodeItem): void {
    const index = items.value.findIndex((item) => item.id === node.id)
    if (index >= 0) items.value[index] = node
  }

  /** Drop one row after it left the visible directory (moved or trashed). */
  function dropNode(id: number): void {
    const index = items.value.findIndex((item) => item.id === id)
    if (index < 0) return
    items.value.splice(index, 1)
    total.value = Math.max(0, total.value - 1)
  }

  /**
   * Rename a node. The request folds in `revision`; a concurrent writer makes
   * the server answer 409 stale_write and the caller refreshes.
   */
  async function renameNode(node: NodeItem, name: string): Promise<NodeItem> {
    const updated = await updateNode(node.id, { name, revision: node.revision })
    replaceNode(updated)
    return updated
  }

  /** Move a node under `parent` (0 = drive root). */
  async function moveNode(node: NodeItem, targetParent: number): Promise<NodeItem> {
    const updated = await updateNode(node.id, { parent: targetParent, revision: node.revision })
    // A node moved into the listed directory appears; one moved away disappears.
    if (updated.parent_id === parent.value) replaceNode(updated)
    else dropNode(updated.id)
    return updated
  }

  /** Soft delete into the recycle bin (quota stays reserved until purge). */
  async function trashNode(node: NodeItem): Promise<NodeItem> {
    const trashed = await deleteNode(node.id)
    dropNode(node.id)
    if (!trashed) return node
    return trashed
  }

  function reset(): void {
    items.value = []
    total.value = 0
    loading.value = false
    error.value = null
    errorCode.value = null
    loaded.value = false
    parent.value = 0
  }

  return {
    items,
    total,
    loading,
    error,
    errorCode,
    loaded,
    parent,
    load,
    createFolder,
    renameNode,
    moveNode,
    trashNode,
    reset,
  }
})
