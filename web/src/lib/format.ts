const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const
const BYTE_BASE = 1000 // decimal units: 1 GB = 1_000_000_000 B

/** Human readable byte size, e.g. 1536 -> "1.54 KB" (decimal units). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'

  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(BYTE_BASE)), BYTE_UNITS.length - 1)
  const value = bytes / BYTE_BASE ** exponent
  const digits = exponent === 0 ? 0 : value >= 100 ? 0 : value >= 10 ? 1 : 2

  return `${value.toFixed(digits)} ${BYTE_UNITS[exponent]}`
}

/** Quota and limits are shown in GB (decimal) regardless of magnitude. */
export function formatGB(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 GB'
  const gb = bytes / 1_000_000_000
  if (gb < 0.01) return '<0.01 GB'
  return `${gb >= 100 ? gb.toFixed(0) : gb >= 10 ? gb.toFixed(1) : gb.toFixed(2)} GB`
}

const dateTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})

/** ISO timestamp -> local zh-CN date string; "—" for unusable input. */
export function formatDateTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  return dateTimeFormatter.format(date)
}

