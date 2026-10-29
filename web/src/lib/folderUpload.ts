/**
 * M3.1 folder upload (docs/12-m3-plan.md §2.4).
 *
 * Browsers expose a directory pick through `File.webkitRelativePath` (picker)
 * or through `DataTransferItem.webkitGetAsEntry()` (drag & drop). Both are
 * turned into "relative path + File" pairs here, then every directory segment
 * is resolved against the node API: an existing directory with the exact name
 * is reused, a missing one is created, and a `name_conflict` triggers a re-list
 * so a concurrent create still resolves to the same folder.
 */
import { ApiError, createNode, listNodes } from '../api/client'
import { messageForError } from './errors'

/** Plan §2.4: a single folder upload creates at most 200 files. */
export const FOLDER_UPLOAD_LIMIT = 200

export interface SelectedFile {
  file: File
  /** Relative path such as `照片/2024/a.jpg`; plain files use their name. */
  path: string
}

export interface UploadEntry {
  file: File
  /** Resolved parent directory id (`0` = drive root). */
  parent: number
  /** Path shown in the upload queue so same-named files stay distinguishable. */
  relativePath: string
}

export interface FolderResolutionFailure {
  path: string
  message: string
}

export interface FolderResolution {
  entries: UploadEntry[]
  failures: FolderResolutionFailure[]
  skippedEmptyDirs: number
}

const DIRECTORY_PAGE_SIZE = 200

function splitPath(path: string): string[] {
  return path
    .split('/')
    .map((segment) => segment.trim())
    .filter((segment) => segment.length > 0 && segment !== '.' && segment !== '..')
}

/** Files chosen through `<input type="file" webkitdirectory>`. */
export function filesFromPicker(list: FileList | null | undefined): SelectedFile[] {
  if (!list) return []
  return Array.from(list).map((file) => ({
    file,
    path: file.webkitRelativePath || file.name,
  }))
}

function fullPathToRelative(fullPath: string, fallbackName: string): string {
  const segments = splitPath(fullPath)
  if (segments.length === 0) return fallbackName
  if (segments[segments.length - 1] === fallbackName) return segments.join('/')
  return [...segments, fallbackName].join('/')
}

async function readDirectory(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => {
    reader.readEntries(resolve, reject)
  })
  return batch
}

async function walkEntry(entry: FileSystemEntry, out: SelectedFile[], counters: { emptyDirs: number }): Promise<void> {
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => {
      ;(entry as FileSystemFileEntry).file(resolve, reject)
    })
    out.push({ file, path: fullPathToRelative(entry.fullPath, file.name) })
    return
  }

  if (!entry.isDirectory) return

  const reader = (entry as FileSystemDirectoryEntry).createReader()
  let sawChild = false

  for (;;) {
    const batch = await readDirectory(reader)
    if (batch.length === 0) break
    sawChild = true
    for (const child of batch) await walkEntry(child, out, counters)
  }

  // `webkitdirectory` never reports empty folders; drag & drop does, and the
  // plan says they are skipped rather than created.
  if (!sawChild) counters.emptyDirs += 1
}

/** Files dropped onto the page, including dropped directories (recursively). */
export async function filesFromDrop(
  dataTransfer: DataTransfer,
): Promise<{ files: SelectedFile[]; skippedEmptyDirs: number }> {
  const items = Array.from(dataTransfer.items ?? [])
  const entries = items
    .map((item) => (typeof item.webkitGetAsEntry === 'function' ? item.webkitGetAsEntry() : null))
    .filter((entry): entry is FileSystemEntry => entry !== null)

  if (entries.length === 0) {
    // Some sources expose files without DataTransfer entries; the picker path
    // already stores the relative path on the File, so keep it when present.
    return {
      files: Array.from(dataTransfer.files ?? []).map((file) => ({
        file,
        path: file.webkitRelativePath || file.name,
      })),
      skippedEmptyDirs: 0,
    }
  }

  const files: SelectedFile[] = []
  const counters = { emptyDirs: 0 }
  for (const entry of entries) await walkEntry(entry, files, counters)
  return { files, skippedEmptyDirs: counters.emptyDirs }
}

interface DirectoryCache {
  ensureChildren(parent: number): Promise<Map<string, number>>
  ensureChild(parent: number, name: string): Promise<number>
}

function createDirectoryCache(): DirectoryCache {
  const childrenByParent = new Map<number, Map<string, number>>()

  async function listChildren(parent: number): Promise<Map<string, number>> {
    const cached = childrenByParent.get(parent)
    if (cached) return cached

    const map = new Map<string, number>()
    const page = await listNodes({ parent, limit: DIRECTORY_PAGE_SIZE, offset: 0 })
    for (const node of page.items) {
      if (node.kind === 'dir' && !map.has(node.name)) map.set(node.name, node.id)
    }
    childrenByParent.set(parent, map)
    return map
  }

  async function ensureChild(parent: number, name: string): Promise<number> {
    const children = await listChildren(parent)
    const existing = children.get(name)
    if (existing !== undefined) return existing

    try {
      const created = await createNode({ parent, name, kind: 'dir' })
      children.set(created.name, created.id)
      return created.id
    } catch (cause) {
      if (cause instanceof ApiError && cause.code === 'name_conflict') {
        // Another tab/upload created it in the meantime: re-list and reuse.
        childrenByParent.delete(parent)
        const refreshed = await listChildren(parent)
        const found = refreshed.get(name)
        if (found !== undefined) return found
      }
      throw cause
    }
  }

  return { ensureChildren: listChildren, ensureChild }
}

/**
 * Creates/reuses the directory chain for every selected file and returns the
 * upload entries. A directory failure only fails the files below it: the
 * caller receives them in `failures` and can keep uploading the rest.
 */
export async function resolveFolderUpload(
  selected: readonly SelectedFile[],
  rootParent: number,
): Promise<FolderResolution> {
  const cache = createDirectoryCache()
  const entries: UploadEntry[] = []
  const failures: FolderResolutionFailure[] = []

  for (const item of selected) {
    const segments = splitPath(item.path)
    if (segments.length === 0) continue

    const relativePath = segments.join('/')
    try {
      let parent = rootParent
      for (const segment of segments.slice(0, -1)) {
        parent = await cache.ensureChild(parent, segment)
      }
      entries.push({ file: item.file, parent, relativePath })
    } catch (cause) {
      failures.push({ path: relativePath, message: messageForError(cause) })
    }
  }

  return { entries, failures, skippedEmptyDirs: 0 }
}
