/**
 * Time values arrive in several shapes depending on the endpoint: RFC3339
 * strings in JSON bodies, unix seconds in `expires_at` columns, and
 * `Retry-After` headers. Parse them defensively and return epoch milliseconds
 * (or seconds, for the header).
 */
export function parseEpoch(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value > 1e12 ? value : value * 1000
  }

  if (typeof value === 'string') {
    const raw = value.trim()
    if (!raw) return null

    const numeric = Number(raw)
    if (Number.isFinite(numeric)) return numeric > 1e12 ? numeric : numeric * 1000

    const millis = Date.parse(raw)
    return Number.isFinite(millis) ? millis : null
  }

  return null
}

/** `Retry-After` is either delta-seconds or an HTTP date. */
export function parseRetryAfter(value: string | null): number | null {
  if (!value) return null

  const raw = value.trim()
  if (!raw) return null

  const seconds = Number(raw)
  if (Number.isFinite(seconds)) return Math.max(0, Math.ceil(seconds))

  const millis = Date.parse(raw)
  if (!Number.isFinite(millis)) return null
  return Math.max(0, Math.ceil((millis - Date.now()) / 1000))
}
