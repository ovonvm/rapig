import type { NodeItem } from '../api/client'

/** Media kinds handled by the dedicated player page (personal space only). */
export type MediaKind = 'audio' | 'video'

const AUDIO_EXT = new Set(['mp3', 'wav', 'ogg', 'oga', 'm4a', 'aac', 'flac', 'opus'])
const VIDEO_EXT = new Set(['mp4', 'webm', 'ogv', 'mov', 'm4v'])

/** Lowercased extension without the dot ('' when the name has none). */
export function extensionOf(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot <= 0 || dot === name.length - 1) return ''
  return name.slice(dot + 1).toLowerCase()
}

/** Filename without its final extension. */
export function fileStem(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot <= 0) return name
  return name.slice(0, dot)
}

/** Which dedicated player (if any) a file should open in. */
export function mediaKind(name: string): MediaKind | null {
  const ext = extensionOf(name)
  if (AUDIO_EXT.has(ext)) return 'audio'
  if (VIDEO_EXT.has(ext)) return 'video'
  return null
}

/** Sidecar naming kinds the player understands. */
export type SidecarKind = 'cover' | 'lyrics' | 'subtitles'

/** Files of the same media kind, in list order (used as the playlist). */
export function mediaSiblings(nodes: NodeItem[], kind: MediaKind): NodeItem[] {
  return nodes.filter((node) => node.kind === 'file' && mediaKind(node.name) === kind)
}

function findByName(nodes: NodeItem[], names: string[]): NodeItem | null {
  const wanted = names.map((name) => name.toLowerCase())
  for (const name of wanted) {
    const hit = nodes.find((node) => node.kind === 'file' && node.name.toLowerCase() === name)
    if (hit) return hit
  }
  return null
}

/**
 * Sidecar file for a media node, by convention:
 * - cover: `<stem>.jpg/png`, `cover.jpg/png`, `folder.jpg/png`
 * - lyrics: `<stem>.lrc`
 * - subtitles: `<stem>.vtt/.srt/.ass/.ssa` (VTT has priority)
 */
export function findSidecar(nodes: NodeItem[], media: NodeItem, kind: SidecarKind): NodeItem | null {
  const stem = fileStem(media.name).toLowerCase()
  if (kind === 'cover') {
    return findByName(nodes, [
      `${stem}.jpg`,
      `${stem}.jpeg`,
      `${stem}.png`,
      `${stem}.webp`,
      'cover.jpg',
      'cover.png',
      'folder.jpg',
      'folder.png',
    ])
  }
  if (kind === 'lyrics') return findByName(nodes, [`${stem}.lrc`])
  return findByName(nodes, [`${stem}.vtt`, `${stem}.srt`, `${stem}.ass`, `${stem}.ssa`])
}

/** Sidecar subtitles only count when the browser can consume them as WebVTT (converted client side). */
export function subtitleFormat(name: string): 'vtt' | 'srt' | 'ass' | 'ssa' | null {
  const ext = extensionOf(name)
  if (ext === 'vtt' || ext === 'srt' || ext === 'ass' || ext === 'ssa') return ext
  return null
}

/** `m:ss` under an hour, `h:mm:ss` above. */
export function formatClock(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00'
  const total = Math.floor(seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60
  if (hours > 0) return `${hours}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  return `${minutes}:${String(secs).padStart(2, '0')}`
}

/** Folder route for a parent id (`0` means the root). */
export function folderRoute(parentId: number): string {
  return parentId > 0 ? `/files/${parentId}` : '/files'
}
