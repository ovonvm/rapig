import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { saveBlob } from '../lib/download'
import {
  readDownloads,
  writeDownloads,
  type PersistedDownload,
  type PersistedDownloadStatus,
} from '../lib/downloadPersist'

export type DownloadStatus = 'queued' | 'downloading' | 'paused' | 'done' | 'failed'

export interface DownloadTask {
  id: number
  name: string
  size: number
  url: string
  status: DownloadStatus
  received: number
  total: number
  error: string | null
  createdAt: number
}

export interface DownloadCandidate {
  url: string
  name: string
  size: number
}

/** Two parallel files keep the browser cooperative without feeling slow. */
const DOWNLOAD_CONCURRENCY = 2
const PERSIST_DELAY_MS = 400
const AUTO_CLEAR_MS = 3000
const PROGRESS_STEP = 256 * 1024

let nextTaskId = 1

function statusLabel(status: DownloadStatus): string {
  if (status === 'queued') return '等待中'
  if (status === 'downloading') return '下载中'
  if (status === 'paused') return '已暂停'
  if (status === 'done') return '已完成'
  return '下载失败'
}

export { statusLabel }

export const useDownloadsStore = defineStore('downloads', () => {
  const tasks = ref<DownloadTask[]>([])
  const panelOpen = ref(false)

  const controllers = new Map<number, AbortController>()
  /** Why a controller was aborted: a pause/cancel that is not a failure. */
  const stopReasons = new Map<number, 'pause' | 'cancel'>()
  let restored = false
  let persistTimer: number | null = null
  let clearTimer: number | null = null

  const activeCount = computed(
    () => tasks.value.filter((task) => task.status === 'queued' || task.status === 'downloading').length,
  )
  const finishedCount = computed(() => tasks.value.filter((task) => task.status === 'done').length)
  const failedCount = computed(() => tasks.value.filter((task) => task.status === 'failed').length)
  const pausedCount = computed(() => tasks.value.filter((task) => task.status === 'paused').length)
  const hasTasks = computed(() => tasks.value.length > 0)

  function toTask(entry: PersistedDownload): DownloadTask {
    const status: DownloadStatus =
      entry.status === 'done' || entry.status === 'failed' ? entry.status : 'paused'
    return {
      id: entry.id,
      name: entry.name,
      size: entry.size,
      url: entry.url,
      status,
      received: entry.received,
      total: entry.total || entry.size,
      error: entry.error,
      createdAt: entry.createdAt,
    }
  }

  /** Reload the persisted list once; tasks come back paused, never running. */
  function restore(): void {
    if (restored) return
    restored = true
    for (const entry of readDownloads()) {
      const task = toTask(entry)
      tasks.value.push(task)
      nextTaskId = Math.max(nextTaskId, task.id + 1)
    }
    // Completed tasks restored from a previous visit should not linger forever.
    if (tasks.value.length > 0) scheduleAutoClear()
  }

  function snapshot(): PersistedDownload[] {
    return tasks.value.map((task) => ({
      id: task.id,
      name: task.name,
      size: task.size,
      url: task.url,
      status: task.status as PersistedDownloadStatus,
      received: task.received,
      total: task.total,
      error: task.error,
      createdAt: task.createdAt,
    }))
  }

  function queuePersist(): void {
    if (persistTimer !== null) return
    persistTimer = window.setTimeout(() => {
      persistTimer = null
      writeDownloads(snapshot())
    }, PERSIST_DELAY_MS)
  }

  /** Write immediately, e.g. before the page goes away or a state change lands. */
  function flushPersist(): void {
    if (persistTimer !== null) {
      window.clearTimeout(persistTimer)
      persistTimer = null
    }
    writeDownloads(snapshot())
  }

  // A reload should not lose the queue, so persist right before the page hides.
  window.addEventListener('pagehide', flushPersist)

  function scheduleAutoClear(): void {
    if (clearTimer !== null) window.clearTimeout(clearTimer)
    if (panelOpen.value) return
    const running = tasks.value.some(
      (task) => task.status === 'queued' || task.status === 'downloading' || task.status === 'paused',
    )
    if (running || tasks.value.length === 0) return

    clearTimer = window.setTimeout(() => {
      clearTimer = null
      if (panelOpen.value) return
      clearFinished()
    }, AUTO_CLEAR_MS)
  }

  function resetClearTimer(): void {
    if (clearTimer !== null) {
      window.clearTimeout(clearTimer)
      clearTimer = null
    }
  }

  async function streamToBlob(task: DownloadTask, signal: AbortSignal): Promise<Blob> {
    const response = await fetch(task.url, { credentials: 'same-origin', signal })
    if (!response.ok) throw new Error(`${response.status}`)

    const header = Number(response.headers.get('content-length'))
    if (Number.isFinite(header) && header > 0) task.total = header
    if (!response.body) return response.blob()

    const reader = response.body.getReader()
    const chunks: BlobPart[] = []
    let received = 0
    let lastStep = 0

    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      if (!value) continue
      chunks.push(value)
      received += value.byteLength
      task.received = received
      if (received - lastStep >= PROGRESS_STEP) {
        lastStep = received
        queuePersist()
      }
    }

    task.received = received
    return new Blob(chunks)
  }

  async function runTask(task: DownloadTask): Promise<void> {
    const controller = new AbortController()
    controllers.set(task.id, controller)
    task.status = 'downloading'
    task.error = null
    queuePersist()

    try {
      const blob = await streamToBlob(task, controller.signal)
      saveBlob(blob, task.name)
      task.status = 'done'
      task.received = task.total || task.received
      stopReasons.delete(task.id)
    } catch (cause) {
      const reason = stopReasons.get(task.id)
      stopReasons.delete(task.id)
      if (reason) {
        task.status = reason === 'pause' ? 'paused' : 'paused'
      } else if (controller.signal.aborted) {
        task.status = 'paused'
      } else {
        task.status = 'failed'
        task.error = cause instanceof Error && cause.message !== '' ? cause.message : '下载失败'
      }
    } finally {
      controllers.delete(task.id)
      queuePersist()
      if (task.status === 'done' || task.status === 'failed') scheduleAutoClear()
    }
  }

  function pump(): void {
    const running = tasks.value.filter((task) => task.status === 'downloading').length
    if (running >= DOWNLOAD_CONCURRENCY) return
    const next = tasks.value.find((task) => task.status === 'queued')
    if (!next) {
      if (running === 0) scheduleAutoClear()
      return
    }
    void runTask(next)
    queuePersist()
  }

  /** Queue files for download; returns how many were added. */
  function enqueue(items: readonly DownloadCandidate[]): number {
    restore()
    resetClearTimer()

    const added: DownloadTask[] = []
    for (const item of items) {
      if (!item.url || !item.name) continue
      added.push({
        id: nextTaskId++,
        name: item.name,
        size: item.size,
        url: item.url,
        status: 'queued',
        received: 0,
        total: item.size,
        error: null,
        createdAt: Date.now(),
      })
    }
    if (added.length === 0) return 0

    tasks.value.push(...added)
    flushPersist()
    pump()
    for (let index = 0; index < added.length; index += 1) pump()
    return added.length
  }

  function pause(id: number): void {
    const task = tasks.value.find((entry) => entry.id === id)
    if (!task || task.status !== 'downloading') return
    stopReasons.set(id, 'pause')
    controllers.get(id)?.abort()
  }

  function resume(id: number): void {
    const task = tasks.value.find((entry) => entry.id === id)
    if (!task || (task.status !== 'paused' && task.status !== 'failed')) return
    task.status = 'queued'
    task.error = null
    task.received = 0
    queuePersist()
    pump()
  }

  function cancel(id: number): void {
    const task = tasks.value.find((entry) => entry.id === id)
    if (!task) return
    if (task.status === 'downloading') {
      stopReasons.set(id, 'cancel')
      controllers.get(id)?.abort()
    }
    tasks.value = tasks.value.filter((entry) => entry.id !== id)
    queuePersist()
    scheduleAutoClear()
    pump()
  }

  function retry(id: number): void {
    resume(id)
  }

  function pauseAll(): void {
    for (const task of tasks.value) {
      if (task.status === 'queued') task.status = 'paused'
      else if (task.status === 'downloading') pause(task.id)
    }
    queuePersist()
    flushPersist()
  }

  function resumeAll(): void {
    for (const task of tasks.value) {
      if (task.status === 'paused' || task.status === 'failed') {
        task.status = 'queued'
        task.error = null
      }
    }
    queuePersist()
    for (let index = 0; index < DOWNLOAD_CONCURRENCY; index += 1) pump()
  }

  function clearFinished(): void {
    tasks.value = tasks.value.filter(
      (task) => task.status !== 'done' && task.status !== 'failed',
    )
    queuePersist()
  }

  function openPanel(): void {
    resetClearTimer()
    panelOpen.value = true
  }

  function closePanel(): void {
    panelOpen.value = false
    scheduleAutoClear()
  }

  function togglePanel(): void {
    if (panelOpen.value) closePanel()
    else openPanel()
  }

  /** Abort everything without deleting the list (used on logout). */
  function reset(): void {
    resetClearTimer()
    for (const controller of controllers.values()) controller.abort()
    controllers.clear()
    stopReasons.clear()
    panelOpen.value = false
    tasks.value = []
    writeDownloads([])
    restored = true
  }

  return {
    tasks,
    panelOpen,
    activeCount,
    finishedCount,
    failedCount,
    pausedCount,
    hasTasks,
    restore,
    enqueue,
    pause,
    resume,
    cancel,
    retry,
    pauseAll,
    resumeAll,
    clearFinished,
    openPanel,
    closePanel,
    togglePanel,
    reset,
  }
})
