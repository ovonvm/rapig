import { reactive } from 'vue'
import { defineStore } from 'pinia'
import { ApiError, listNodes } from '../api/client'
import type { NodeItem } from '../api/client'

const CHILD_LIMIT = 200

/**
 * Lazy directory cache used by the move dialog: every parent id is fetched at
 * most once per dialog session, and only directories are kept.
 */
export const useFoldersStore = defineStore('folders', () => {
  const children = reactive(new Map<number, NodeItem[]>())
  const loading = reactive(new Set<number>())
  const errors = reactive(new Map<number, string>())

  function childrenOf(parent: number): NodeItem[] {
    return children.get(parent) ?? []
  }

  function isLoading(parent: number): boolean {
    return loading.has(parent)
  }

  function errorOf(parent: number): string | null {
    return errors.get(parent) ?? null
  }

  function hasLoaded(parent: number): boolean {
    return children.has(parent)
  }

  async function loadChildren(parent: number): Promise<void> {
    if (children.has(parent) || loading.has(parent)) return
    loading.add(parent)
    errors.delete(parent)

    try {
      const page = await listNodes({ parent, limit: CHILD_LIMIT, offset: 0 })
      children.set(
        parent,
        page.items.filter((item) => item.kind === 'dir'),
      )
    } catch (cause) {
      errors.set(parent, cause instanceof ApiError ? cause.message : '加载失败，请稍后重试')
    } finally {
      loading.delete(parent)
    }
  }

  function reset(): void {
    children.clear()
    loading.clear()
    errors.clear()
  }

  return { children, loading, childrenOf, isLoading, errorOf, hasLoaded, loadChildren, reset }
})
