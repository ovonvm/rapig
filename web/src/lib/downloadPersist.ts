export type PersistedDownloadStatus = 'queued' | 'downloading' | 'paused' | 'done' | 'failed'

export interface PersistedDownload {
  id: number
  name: string
  size: number
  url: string
  status: PersistedDownloadStatus
  received: number
  total: number
  error: string | null
  createdAt: number
}

export const DOWNLOAD_STORAGE_KEY = 'rapig.downloads.v1'

const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000
const STATUSES: PersistedDownloadStatus[] = ['queued', 'downloading', 'paused', 'done', 'failed']

function toEntry(value: unknown): PersistedDownload | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const id = typeof raw.id === 'number' && Number.isFinite(raw.id) ? Math.trunc(raw.id) : null
  const name = typeof raw.name === 'string' ? raw.name : ''
  const url = typeof raw.url === 'string' ? raw.url : ''
  if (id === null || id <= 0 || name === '' || url === '') return null

  const status = typeof raw.status === 'string' && (STATUSES as string[]).includes(raw.status)
    ? (raw.status as PersistedDownloadStatus)
    : 'paused'
  const size = typeof raw.size === 'number' && raw.size > 0 ? raw.size : 0
  const received = typeof raw.received === 'number' && raw.received >= 0 ? raw.received : 0
  const total = typeof raw.total === 'number' && raw.total >= 0 ? raw.total : size
  const error = typeof raw.error === 'string' ? raw.error : null
  const createdAt = typeof raw.createdAt === 'number' && raw.createdAt > 0 ? raw.createdAt : Date.now()

  return { id, name, size, url, status, received, total, error, createdAt }
}

export function readDownloads(): PersistedDownload[] {
  try {
    const raw = window.localStorage.getItem(DOWNLOAD_STORAGE_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []

    const now = Date.now()
    const entries: PersistedDownload[] = []
    for (const value of parsed) {
      const entry = toEntry(value)
      if (!entry) continue
      if (now - entry.createdAt > MAX_AGE_MS) continue
      entries.push(entry)
    }
    return entries.slice(0, 50)
  } catch {
    return []
  }
}

/** Persisted tasks never run on their own: they come back paused. */
export function writeDownloads(entries: readonly PersistedDownload[]): void {
  try {
    if (entries.length === 0) {
      window.localStorage.removeItem(DOWNLOAD_STORAGE_KEY)
      return
    }
    window.localStorage.setItem(DOWNLOAD_STORAGE_KEY, JSON.stringify(entries.slice(0, 50)))
  } catch {
    // Private mode or a full quota: task persistence is a nicety, not a need.
  }
}

export function clearPersistedDownloads(): void {
  try {
    window.localStorage.removeItem(DOWNLOAD_STORAGE_KEY)
  } catch {
    // Ignore unavailable storage.
  }
}
