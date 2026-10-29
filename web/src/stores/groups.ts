import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { ApiError, createGroup, listMyGroups } from '../api/client'
import type { CreateGroupInput, CreatedGroup, MyGroup } from '../api/client'
import { errorCodeOf, messageForError } from '../lib/errors'

/**
 * 「组空间」 sidebar data source (P1): the groups the signed-in user belongs
 * to. It is loaded once per session and refreshed after creating a group.
 */
export const useGroupsStore = defineStore('groups', () => {
  const groups = ref<MyGroup[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref<string | null>(null)
  const errorCode = ref<string | null>(null)

  const hasGroups = computed(() => groups.value.length > 0)

  let sequence = 0

  async function load(): Promise<void> {
    const current = ++sequence
    loading.value = true
    error.value = null
    errorCode.value = null

    try {
      const items = await listMyGroups()
      if (current !== sequence) return
      groups.value = items
      loaded.value = true
    } catch (cause) {
      if (current !== sequence) return
      groups.value = []
      error.value = messageForError(cause, '组空间加载失败，请稍后重试')
      errorCode.value = errorCodeOf(cause)
    } finally {
      if (current === sequence) loading.value = false
    }
  }

  /** Used after create/join/leave so the sidebar matches the server. */
  async function refresh(): Promise<void> {
    loaded.value = false
    await load()
  }

  async function create(input: CreateGroupInput): Promise<CreatedGroup> {
    const created = await createGroup(input)
    await load()
    return created
  }

  function byId(id: number): MyGroup | null {
    return groups.value.find((group) => group.id === id) ?? null
  }

  function reset(): void {
    sequence += 1
    groups.value = []
    loading.value = false
    loaded.value = false
    error.value = null
    errorCode.value = null
  }

  return { groups, loading, loaded, error, errorCode, hasGroups, load, refresh, create, byId, reset }
})

/** 404 while loading a group page: not a member (or the group is gone). */
export function isGroupNotFound(cause: unknown): boolean {
  return cause instanceof ApiError && (cause.code === 'not_found' || cause.status === 404)
}
