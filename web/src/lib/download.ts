/**
 * Save a fetched blob through a temporary anchor. Browsers treat these as
 * ordinary downloads, so no extra API is involved.
 */
export function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename || 'download'
  link.style.display = 'none'
  document.body.append(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

/**
 * Folder shares can only be saved one request per file. Keep the fan-out small
 * enough that a browser does not start dropping automatic downloads.
 */
export const PACK_DOWNLOAD_LIMIT = 5
