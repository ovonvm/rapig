/**
 * Breadcrumb cache for the folder tree.
 *
 * The API has no `GET /nodes/{id}` lookup, so the only way to name a directory
 * above the visible listing is to remember the path the user walked while
 * browsing. Entries live in `sessionStorage`: one tab's trail is enough, and a
 * reload keeps the breadcrumb intact without asking the server.
 */
export const PATHS_STORAGE_KEY = 'rapig.paths.v1'

export interface PathEntry {
  name: string
  parent: number
}

export interface PathCrumb {
  id: number
  name: string
}

export const ROOT_CRUMB: PathCrumb = { id: 0, name: '我的文件' }

/** Placeholder used when the trail does not know a directory's name. */
export function fallbackName(id: number): string {
  return `目录 #${id}`
}

function isEntry(value: unknown): value is PathEntry {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Partial<PathEntry>
  return (
    typeof candidate.name === 'string' &&
    candidate.name.length > 0 &&
    typeof candidate.parent === 'number' &&
    Number.isInteger(candidate.parent) &&
    candidate.parent >= 0
  )
}

export function readPaths(): Record<string, PathEntry> {
  try {
    const raw = window.sessionStorage.getItem(PATHS_STORAGE_KEY)
    if (!raw) return {}
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return {}

    const paths: Record<string, PathEntry> = {}
    for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (isEntry(value)) paths[key] = { name: value.name, parent: value.parent }
    }
    return paths
  } catch {
    // Private mode / disabled storage: breadcrumbs simply lose their history.
    return {}
  }
}

function writePaths(paths: Record<string, PathEntry>): void {
  try {
    window.sessionStorage.setItem(PATHS_STORAGE_KEY, JSON.stringify(paths))
  } catch {
    // Keep the in-memory navigation working even when storage is blocked.
  }
}

/** Record that directory `id` lives under `parent` and is called `name`. */
export function rememberPath(id: number, name: string, parent: number): void {
  if (!Number.isInteger(id) || id <= 0 || name.length === 0) return
  const paths = readPaths()
  const parentId = Number.isInteger(parent) && parent > 0 ? parent : 0
  paths[String(id)] = { name, parent: parentId }
  writePaths(paths)
}

/** Keep the stored name in sync after a rename (the id does not change). */
export function renamePath(id: number, name: string): void {
  if (name.length === 0) return
  const paths = readPaths()
  const entry = paths[String(id)]
  if (!entry || entry.name === name) return
  entry.name = name
  writePaths(paths)
}

/** Drop a directory that no longer exists (trashed). */
export function forgetPath(id: number): void {
  const paths = readPaths()
  if (!(String(id) in paths)) return
  delete paths[String(id)]
  writePaths(paths)
}

export function parentOfPath(id: number): number {
  return readPaths()[String(id)]?.parent ?? 0
}

/**
 * Root-first breadcrumb trail for `id`. Unknown directories render as
 * `目录 #id`, and an unknown ancestor stops the walk at the root, so a deep
 * link still shows a usable (if shorter) trail.
 */
export function pathChain(id: number): PathCrumb[] {
  if (!Number.isInteger(id) || id <= 0) return [ROOT_CRUMB]

  const paths = readPaths()
  const trail: PathCrumb[] = []
  const seen = new Set<number>([0])

  let cursor = id
  while (cursor > 0 && !seen.has(cursor)) {
    seen.add(cursor)
    const entry = paths[String(cursor)]
    trail.push({ id: cursor, name: entry?.name ?? fallbackName(cursor) })
    if (!entry) break
    cursor = entry.parent
  }

  trail.reverse()
  return [ROOT_CRUMB, ...trail]
}
