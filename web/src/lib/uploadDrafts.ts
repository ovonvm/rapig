/**
 * Files picked beyond the first batch are kept as metadata-only drafts: a File
 * handle cannot survive a reload, so the user re-picks the files and we match
 * them by name and size.
 */
export interface DraftFile {
  name: string
  size: number
  lastModified: number
}

export interface UploadDraft {
  parent: number
  createdAt: number
  files: DraftFile[]
}

export const DRAFTS_STORAGE_KEY = 'rapig.drafts.v1'

/** Pick at most this many files straight away, draft the rest. */
export const MAX_QUEUED_FILES = 5

const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000

function toDraftFile(value: unknown): DraftFile | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  if (typeof raw.name !== 'string' || raw.name === '') return null
  const size = typeof raw.size === 'number' && raw.size >= 0 ? raw.size : 0
  const lastModified = typeof raw.lastModified === 'number' && raw.lastModified > 0 ? raw.lastModified : 0
  return { name: raw.name, size, lastModified }
}

export function readDrafts(): UploadDraft | null {
  try {
    const raw = window.localStorage.getItem(DRAFTS_STORAGE_KEY)
    if (!raw) return null
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object') return null

    const record = parsed as Record<string, unknown>
    const createdAt = typeof record.createdAt === 'number' ? record.createdAt : 0
    if (createdAt <= 0 || Date.now() - createdAt > MAX_AGE_MS) {
      clearDrafts()
      return null
    }

    const draftFiles: DraftFile[] = []
    if (Array.isArray(record.files)) {
      for (const value of record.files) {
        const file = toDraftFile(value)
        if (file) draftFiles.push(file)
      }
    }
    if (draftFiles.length === 0) return null

    const parent = typeof record.parent === 'number' && record.parent > 0 ? Math.trunc(record.parent) : 0
    return { parent, createdAt, files: draftFiles }
  } catch {
    return null
  }
}

export function saveDrafts(files: readonly File[], parent: number): void {
  if (files.length === 0) return
  saveDraftList(
    files.map((file) => ({ name: file.name, size: file.size, lastModified: file.lastModified })),
    parent,
  )
}

/** Persist metadata that is already in draft form (used after a partial re-pick). */
export function saveDraftList(files: readonly DraftFile[], parent: number): void {
  if (files.length === 0) {
    clearDrafts()
    return
  }
  const draft: UploadDraft = {
    parent,
    createdAt: Date.now(),
    files: files.map((file) => ({ name: file.name, size: file.size, lastModified: file.lastModified })),
  }
  try {
    window.localStorage.setItem(DRAFTS_STORAGE_KEY, JSON.stringify(draft))
  } catch {
    // No storage: the files are simply not remembered.
  }
}

export function clearDrafts(): void {
  try {
    window.localStorage.removeItem(DRAFTS_STORAGE_KEY)
  } catch {
    // Ignore unavailable storage.
  }
}

export interface DraftMatch {
  matched: File[]
  missing: DraftFile[]
}

/** Match re-picked files against the draft list, preserving draft order. */
export function matchDrafts(files: readonly File[], drafts: readonly DraftFile[]): DraftMatch {
  const pool = files.slice()
  const matched: File[] = []
  const missing: DraftFile[] = []

  for (const draft of drafts) {
    const exact = pool.findIndex(
      (file) => file.name === draft.name && file.size === draft.size && (!draft.lastModified || file.lastModified === draft.lastModified),
    )
    const loose = exact === -1 ? pool.findIndex((file) => file.name === draft.name && file.size === draft.size) : exact
    if (loose === -1) {
      missing.push(draft)
      continue
    }
    const file = pool.splice(loose, 1)[0]
    if (file) matched.push(file)
  }

  return { matched, missing }
}
