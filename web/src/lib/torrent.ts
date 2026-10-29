/** `.torrent` upload helpers for the offline download dialog (no network). */

/** Server limit for a `.torrent` upload. */
export const TORRENT_MAX_BYTES = 20 * 1024 * 1024

export type TorrentCheck = { ok: true } | { ok: false; message: string }

function hasTorrentExtension(name: string): boolean {
  return /\.torrent$/i.test(name.trim())
}

/**
 * Cheap client-side gate before the upload: the server parses the file and
 * reports `invalid_torrent`, but a wrong pick should fail instantly.
 */
export function validateTorrentFile(file: File | null | undefined): TorrentCheck {
  if (!file) return { ok: false, message: '请选择种子文件' }
  if (!hasTorrentExtension(file.name)) return { ok: false, message: '请选择 .torrent 种子文件' }
  if (file.size <= 0) return { ok: false, message: '种子文件为空，请重新选择' }
  if (file.size > TORRENT_MAX_BYTES) return { ok: false, message: '种子文件不能超过 20 MB' }
  return { ok: true }
}
