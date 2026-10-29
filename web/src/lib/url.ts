/** URL helpers for the offline download UI (no network access). */

/**
 * Host shown in the transfer list. Only the host is displayed so a long signed
 * URL cannot break the row; the full URL stays available via `title`.
 */
export function hostOf(url: string): string {
  try {
    const parsed = new URL(url)
    return parsed.host || parsed.protocol.replace(':', '')
  } catch {
    const trimmed = url.trim()
    const match = /^(?:[a-zA-Z][a-zA-Z0-9+.-]*:\/\/)?([^/?#]+)/.exec(trimmed)
    return match?.[1] ?? trimmed
  }
}

export type UrlCheck =
  | { ok: true; url: string }
  | { ok: false; message: string }

/** The backend only accepts absolute http(s) URLs, so check that up front. */
export function validateTransferUrl(raw: string): UrlCheck {
  const value = raw.trim()
  if (value === '') return { ok: false, message: '请输入下载链接' }

  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return { ok: false, message: '请输入有效的链接（以 http:// 或 https:// 开头）' }
  }

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { ok: false, message: '只支持 HTTP 或 HTTPS 链接' }
  }
  if (!parsed.hostname) return { ok: false, message: '请输入有效的链接（缺少域名）' }

  return { ok: true, url: parsed.toString() }
}

/** Backend limit for the magnet string. */
export const MAGNET_MAX_LENGTH = 2048

const MAGNET_BTIH = /xt=urn:btih:([a-z2-7]{32}|[0-9a-f]{40})/i

/**
 * Magnet links are opaque URIs, so only the shape the backend can parse is
 * checked here: `magnet:?xt=urn:btih:<info hash>`.
 */
export function validateMagnet(raw: string): UrlCheck {
  const value = raw.trim()
  if (value === '') return { ok: false, message: '请输入磁力链接' }
  if (value.length > MAGNET_MAX_LENGTH) {
    return { ok: false, message: `磁力链接过长（最多 ${MAGNET_MAX_LENGTH} 个字符）` }
  }
  if (!/^magnet:\?/i.test(value)) return { ok: false, message: '磁力链接格式不正确' }
  if (!MAGNET_BTIH.test(value)) {
    return { ok: false, message: '磁力链接格式不正确（缺少 BTIH 信息哈希）' }
  }
  return { ok: true, url: value }
}
