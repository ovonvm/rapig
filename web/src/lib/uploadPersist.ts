/**
 * M3.1 cross-refresh upload resume (docs/12-m3-plan.md §2.5).
 *
 * Unfinished upload sessions are mirrored into localStorage so a page reload
 * does not lose the server-side upload id or the parts that already landed.
 * Only *unfinished* sessions are stored: the entry is deleted as soon as the
 * upload is completed, cancelled, deduped or expires.
 *
 * There is no backend endpoint for this: the completed-part list is trusted
 * from here, and retransmitting a part is harmless because `PUT parts/{n}` is
 * idempotent.
 */

export const UPLOAD_STORAGE_KEY = 'rapig.uploads.v1'

export interface PersistedUpload {
  uploadId: string
  name: string
  parent: number
  size: number
  partSize: number
  partsTotal: number
  sha256: string | null
  doneParts: number[]
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function toEntry(value: unknown): PersistedUpload | null {
  if (!value || typeof value !== 'object') return null
  const record = value as Record<string, unknown>
  const { uploadId, name, parent, size, partSize, partsTotal, sha256, doneParts } = record

  if (typeof uploadId !== 'string' || uploadId.length === 0) return null
  if (typeof name !== 'string' || name.length === 0) return null
  if (!isFiniteNumber(parent) || parent < 0) return null
  if (!isFiniteNumber(size) || size < 0) return null
  if (!isFiniteNumber(partSize) || partSize <= 0) return null
  if (!isFiniteNumber(partsTotal) || partsTotal <= 0) return null
  if (sha256 !== null && typeof sha256 !== 'string') return null
  if (!Array.isArray(doneParts)) return null

  const parts = doneParts.filter(
    (part): part is number =>
      isFiniteNumber(part) && Number.isInteger(part) && part >= 0 && part < partsTotal,
  )

  return {
    uploadId,
    name,
    parent: Math.trunc(parent),
    size,
    partSize: Math.trunc(partSize),
    partsTotal: Math.trunc(partsTotal),
    sha256: sha256 ?? null,
    doneParts: Array.from(new Set(parts)).sort((a, b) => a - b),
  }
}

export function readUploads(): PersistedUpload[] {
  try {
    const raw = window.localStorage.getItem(UPLOAD_STORAGE_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.map(toEntry).filter((entry): entry is PersistedUpload => entry !== null)
  } catch {
    return []
  }
}

function writeUploads(entries: PersistedUpload[]): void {
  try {
    if (entries.length === 0) {
      window.localStorage.removeItem(UPLOAD_STORAGE_KEY)
      return
    }
    window.localStorage.setItem(UPLOAD_STORAGE_KEY, JSON.stringify(entries))
  } catch {
    // Storage can be full or disabled; resuming is best-effort.
  }
}

export function upsertUpload(entry: PersistedUpload): void {
  const entries = readUploads()
  const index = entries.findIndex((item) => item.uploadId === entry.uploadId)
  if (index >= 0) entries[index] = entry
  else entries.push(entry)
  writeUploads(entries)
}

export function removeUpload(uploadId: string): void {
  const entries = readUploads()
  const kept = entries.filter((item) => item.uploadId !== uploadId)
  if (kept.length !== entries.length) writeUploads(kept)
}
