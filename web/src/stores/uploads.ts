import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  ApiError,
  cancelUpload,
  completeUpload,
  initUpload,
  putUploadPart,
} from '../api/client'
import { PREHASH_LIMIT_BYTES, sha256Hex } from '../lib/hash'
import { errorCodeOf, isUnauthenticated, messageForError } from '../lib/errors'
import { parseEpoch } from '../lib/time'
import { readUploads, removeUpload, upsertUpload } from '../lib/uploadPersist'
import { router } from '../router'
import { useAuthStore } from './auth'
import { useFilesStore } from './files'
import { useToast } from '../composables/useToast'

export type UploadStatus =
  | 'pending'
  | 'queued'
  | 'hashing'
  | 'uploading'
  | 'completing'
  | 'done'
  | 'deduped'
  | 'error'
  | 'canceled'

export interface UploadTask {
  id: number
  /** `null` while awaiting the user to re-pick the file for a resumed upload. */
  file: File | null
  /** File name sent to the server (base name). */
  name: string
  /** Queue label: relative path for folder uploads, otherwise the file name. */
  label: string
  size: number
  /** Resolved destination directory id; `0` is the drive root. */
  parent: number
  relativePath: string | null
  status: UploadStatus
  percent: number
  uploadId: string | null
  partSize: number
  partsTotal: number
  expiresAt: number | null
  sha256: string | null
  error: string | null
  errorCode: string | null
  canceled: boolean
  /** True when this task came back from localStorage and needs a file again. */
  resumed: boolean
  receivedBytes: number
  uploadedParts: Set<number>
}

export interface EnqueueEntry {
  file: File
  parent?: number
  relativePath?: string | null
}

/** M1 upload tuning: one file at a time, three parallel parts (docs/09-m1-plan.md §11). */
const PART_CONCURRENCY = 3
const PART_ATTEMPTS = 3
const RETRY_BASE_DELAY = 400
/** Codes after which resuming the same server upload is pointless. */
const RESTART_CODES = new Set(['invalid_state', 'checksum_mismatch', 'not_found', 'payload_too_large'])

const RESUME_MISMATCH_MESSAGE = '文件与上次不同，已取消续传'
const RESUME_EXPIRED_MESSAGE = '续传已过期，请重新上传'

const delay = (millis: number): Promise<void> =>
  new Promise((resolve) => {
    window.setTimeout(resolve, millis)
  })

function throwIfCanceled(task: UploadTask): void {
  if (task.canceled) throw new ApiError(0, 'aborted', '请求已取消')
}

function isExpired(task: UploadTask): boolean {
  return task.expiresAt !== null && task.expiresAt <= Date.now()
}

function percentOf(task: UploadTask): number {
  if (task.size <= 0) return 0
  return Math.min(99, Math.max(0, Math.round((task.receivedBytes / task.size) * 100)))
}

function partLength(size: number, partSize: number, partNo: number): number {
  const start = partNo * partSize
  return Math.max(0, Math.min(start + partSize, size) - start)
}

function bytesOf(size: number, partSize: number, parts: Iterable<number>): number {
  let total = 0
  for (const partNo of parts) total += partLength(size, partSize, partNo)
  return total
}

export const useUploadsStore = defineStore('uploads', () => {
  const tasks = ref<UploadTask[]>([])
  const controllers = new Map<number, AbortController>()

  const files = useFilesStore()
  const auth = useAuthStore()
  const toast = useToast()

  let nextId = 1
  let pumpActive = false
  let restored = false

  const hasTasks = computed(() => tasks.value.length > 0)
  const activeCount = computed(
    () =>
      tasks.value.filter((task) =>
        ['queued', 'hashing', 'uploading', 'completing'].includes(task.status),
      ).length,
  )
  const pendingCount = computed(() => tasks.value.filter((task) => task.status === 'pending').length)
  const hasFinished = computed(() =>
    tasks.value.some((task) => ['done', 'deduped', 'canceled'].includes(task.status)),
  )

  function baseTask(entry: {
    file: File | null
    name: string
    label: string
    size: number
    parent: number
    relativePath: string | null
  }): UploadTask {
    return {
      id: nextId++,
      file: entry.file,
      name: entry.name,
      label: entry.label,
      size: entry.size,
      parent: entry.parent,
      relativePath: entry.relativePath,
      status: 'queued',
      percent: 0,
      uploadId: null,
      partSize: 0,
      partsTotal: 0,
      expiresAt: null,
      sha256: null,
      error: null,
      errorCode: null,
      canceled: false,
      resumed: false,
      receivedBytes: 0,
      uploadedParts: new Set<number>(),
    }
  }

  function findTask(id: number): UploadTask | undefined {
    return tasks.value.find((task) => task.id === id)
  }

  function dropTask(task: UploadTask): void {
    controllers.get(task.id)?.abort()
    controllers.delete(task.id)
    tasks.value = tasks.value.filter((item) => item.id !== task.id)
  }

  function persistTask(task: UploadTask): void {
    if (!task.uploadId || task.partsTotal <= 0 || task.partSize <= 0) return
    if (['done', 'deduped', 'canceled', 'pending'].includes(task.status)) return
    upsertUpload({
      uploadId: task.uploadId,
      name: task.name,
      parent: task.parent,
      size: task.size,
      partSize: task.partSize,
      partsTotal: task.partsTotal,
      sha256: task.sha256,
      doneParts: Array.from(task.uploadedParts).sort((a, b) => a - b),
    })
  }

  function forget(task: UploadTask): void {
    if (task.uploadId) removeUpload(task.uploadId)
  }

  function enqueue(entries: readonly EnqueueEntry[]): void {
    if (entries.length === 0) return

    for (const entry of entries) {
      const relativePath = entry.relativePath ?? null
      const fileName = entry.file.name || '未命名文件'
      tasks.value.push(
        baseTask({
          file: entry.file,
          name: fileName,
          label: relativePath ?? fileName,
          size: entry.file.size,
          parent: entry.parent ?? 0,
          relativePath,
        }),
      )
    }

    void pump()
  }

  /** Queue plain files into `parent` (the directory currently open). */
  function addFiles(list: FileList | File[] | null | undefined, parent = 0): void {
    if (!list) return
    enqueue(Array.from(list).map((file) => ({ file, parent })))
  }

  /**
   * Reads unfinished upload sessions from localStorage (M3.1 §2.5). They show
   * up as `pending` tasks and never touch the network until the user resumes.
   */
  function restorePersisted(): void {
    if (restored) return
    restored = true

    for (const entry of readUploads()) {
      if (tasks.value.some((task) => task.uploadId === entry.uploadId)) continue

      const uploadedParts = new Set(entry.doneParts)
      const receivedBytes = bytesOf(entry.size, entry.partSize, uploadedParts)
      const task = baseTask({
        file: null,
        name: entry.name,
        label: entry.name,
        size: entry.size,
        parent: entry.parent,
        relativePath: null,
      })
      task.status = 'pending'
      task.uploadId = entry.uploadId
      task.partSize = entry.partSize
      task.partsTotal = entry.partsTotal
      task.sha256 = entry.sha256
      task.uploadedParts = uploadedParts
      task.receivedBytes = receivedBytes
      task.percent = percentOf(task)
      task.resumed = true
      tasks.value.push(task)
    }
  }

  /** The user re-picked the file for a pending task; verify it before resuming. */
  async function resumeWithFile(id: number, file: File): Promise<void> {
    const task = findTask(id)
    if (!task || task.status !== 'pending') return

    const cancelResume = (): void => {
      const staleUploadId = task.uploadId
      forget(task)
      dropTask(task)
      if (staleUploadId) void cancelUpload(staleUploadId).catch(() => undefined)
      toast.push(RESUME_MISMATCH_MESSAGE, 'error')
    }

    if (file.name !== task.name || file.size !== task.size) {
      cancelResume()
      return
    }

    if (task.sha256) {
      try {
        const digest = await sha256Hex(file)
        if (digest !== task.sha256) {
          cancelResume()
          return
        }
      } catch {
        // Hashing is unavailable; name + size is the best available check.
      }
    }

    task.file = file
    task.status = 'queued'
    task.error = null
    task.errorCode = null
    task.canceled = false
    void pump()
  }

  async function pump(): Promise<void> {
    if (pumpActive) return
    pumpActive = true

    try {
      for (;;) {
        const next = tasks.value.find((task) => task.status === 'queued')
        if (!next) break
        await processTask(next)
      }
    } finally {
      pumpActive = false
    }
  }

  async function afterUploaded(): Promise<void> {
    await Promise.allSettled([files.load(), auth.refreshUser()])
  }

  async function handleSessionLost(): Promise<void> {
    auth.clearUser()
    files.reset()
    toast.push('登录状态已失效，请重新登录', 'error')
    await router.replace({ name: 'login' })
  }

  async function processTask(task: UploadTask): Promise<void> {
    const controller = new AbortController()
    controllers.set(task.id, controller)

    try {
      task.error = null
      task.errorCode = null
      task.canceled = false
      task.status = 'hashing'
      task.percent = percentOf(task)

      if (!task.file) throw new ApiError(0, 'invalid_state', '缺少文件，无法上传')

      if (!task.sha256 && task.size > 0 && task.size <= PREHASH_LIMIT_BYTES) {
        try {
          task.sha256 = await sha256Hex(task.file)
        } catch {
          // Hashing is an optimisation only; continue without a digest.
          task.sha256 = null
        }
      }

      throwIfCanceled(task)

      if (!task.uploadId || isExpired(task)) {
        task.uploadId = null
        task.uploadedParts = new Set<number>()
        task.receivedBytes = 0
        task.percent = 0
        task.resumed = false

        const init = await initUpload({
          parent: task.parent,
          name: task.name,
          size: task.size,
          sha256: task.sha256 ?? undefined,
        })

        throwIfCanceled(task)

        if (typeof init.deduped_node_id === 'number') {
          task.status = 'deduped'
          task.percent = 100
          task.receivedBytes = task.size
          toast.push(`「${task.name}」已秒传`)
          await afterUploaded()
          return
        }

        task.uploadId = init.upload_id
        task.partSize = init.part_size
        task.partsTotal = init.parts_total
        task.expiresAt = parseEpoch(init.expires_at)
        persistTask(task)
      }

      if (!task.uploadId) throw new ApiError(0, 'invalid_response', '服务器未返回上传编号')

      task.status = 'uploading'
      await uploadParts(task, controller.signal)
      throwIfCanceled(task)

      task.status = 'completing'
      const node = await completeUpload(task.uploadId)

      forget(task)
      task.status = 'done'
      task.percent = 100
      toast.push(`已上传「${node.name}」`)
      await afterUploaded()
    } catch (cause) {
      if (task.canceled || (cause instanceof ApiError && cause.code === 'aborted')) {
        task.status = 'canceled'
        return
      }

      // A resumed session whose server-side TTL elapsed: drop the entry.
      if (
        task.resumed &&
        cause instanceof ApiError &&
        cause.code === 'invalid_state' &&
        cause.reason === 'upload_expired'
      ) {
        forget(task)
        dropTask(task)
        toast.push(RESUME_EXPIRED_MESSAGE, 'error')
        return
      }

      task.status = 'error'
      task.errorCode = errorCodeOf(cause)
      task.error = messageForError(cause)

      if (task.errorCode && RESTART_CODES.has(task.errorCode)) {
        forget(task)
        task.uploadId = null
        task.resumed = false
        task.uploadedParts = new Set<number>()
        task.receivedBytes = 0
        task.percent = 0
      }

      if (isUnauthenticated(cause)) await handleSessionLost()
    } finally {
      controllers.delete(task.id)
    }
  }

  async function uploadParts(task: UploadTask, signal: AbortSignal): Promise<void> {
    if (!task.uploadId) return
    if (task.partsTotal <= 0 || task.partSize <= 0) {
      task.percent = 100
      return
    }

    const pending: number[] = []
    for (let partNo = 0; partNo < task.partsTotal; partNo += 1) {
      if (!task.uploadedParts.has(partNo)) pending.push(partNo)
    }

    task.percent = percentOf(task)

    let cursor = 0
    const worker = async (): Promise<void> => {
      for (;;) {
        if (signal.aborted) throw new ApiError(0, 'aborted', '请求已取消')

        const index = cursor
        cursor += 1
        if (index >= pending.length) return

        const partNo = pending[index]
        if (partNo === undefined) return

        const start = partNo * task.partSize
        const end = Math.min(start + task.partSize, task.size)
        const chunk = task.file!.slice(start, end)
        await putPartWithRetry(task, partNo, chunk, start, end, signal)

        task.uploadedParts.add(partNo)
        task.receivedBytes += end - start
        task.percent = percentOf(task)
        persistTask(task)
      }
    }

    const workerCount = Math.min(PART_CONCURRENCY, pending.length)
    await Promise.all(Array.from({ length: workerCount }, () => worker()))
  }

  async function putPartWithRetry(
    task: UploadTask,
    partNo: number,
    chunk: Blob,
    start: number,
    end: number,
    signal: AbortSignal,
  ): Promise<void> {
    if (!task.uploadId) throw new ApiError(0, 'invalid_state', '上传编号缺失')

    for (let attempt = 1; ; attempt += 1) {
      try {
        await putUploadPart(task.uploadId, partNo, chunk, start, end - 1, task.size, signal)
        return
      } catch (cause) {
        if (attempt >= PART_ATTEMPTS) throw cause
        if (cause instanceof ApiError) {
          if (cause.code === 'aborted' || signal.aborted) throw cause
          const retryable = cause.status === 0 || cause.status >= 500 || cause.status === 429
          if (!retryable) throw cause
        }
        await delay(RETRY_BASE_DELAY * attempt)
      }
    }
  }

  function cancel(id: number): void {
    const task = findTask(id)
    if (!task) return
    if (['done', 'deduped', 'error', 'canceled'].includes(task.status)) return

    if (task.status === 'pending') {
      forget(task)
      const uploadId = task.uploadId
      dropTask(task)
      if (uploadId) void cancelUpload(uploadId).catch(() => undefined)
      return
    }

    task.canceled = true
    task.status = 'canceled'
    controllers.get(id)?.abort()

    const uploadId = task.uploadId
    forget(task)
    task.uploadId = null
    if (uploadId) void cancelUpload(uploadId).catch(() => undefined)
  }

  function retry(id: number): void {
    const task = findTask(id)
    if (!task) return
    if (task.status !== 'error' && task.status !== 'canceled') return
    if (!task.file) return

    task.canceled = false
    task.error = null
    task.errorCode = null
    task.status = 'queued'

    if (isExpired(task)) {
      forget(task)
      task.uploadId = null
      task.uploadedParts = new Set<number>()
      task.receivedBytes = 0
      task.percent = 0
    }

    void pump()
  }

  function clearFinished(): void {
    tasks.value = tasks.value.filter((task) => task.status !== 'done' && task.status !== 'deduped' && task.status !== 'canceled')
  }

  /**
   * Called on logout: aborts everything in flight and empties the queue. The
   * localStorage entries stay, so the same user can resume after re-login.
   */
  function reset(): void {
    for (const task of tasks.value) {
      task.canceled = true
      controllers.get(task.id)?.abort()
      task.uploadId = null
    }
    controllers.clear()
    pumpActive = false
    tasks.value = []
  }

  return {
    tasks,
    hasTasks,
    activeCount,
    pendingCount,
    hasFinished,
    addFiles,
    enqueue,
    restorePersisted,
    resumeWithFile,
    cancel,
    retry,
    clearFinished,
    reset,
  }
})
