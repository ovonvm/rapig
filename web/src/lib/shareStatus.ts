import type { ShareInfo } from '../api/client'
import { formatDateTime } from './format'
import { parseEpoch } from './time'

export type ShareStatus = 'active' | 'expired' | 'revoked' | 'exhausted'

export const SHARE_STATUS_LABELS: Record<ShareStatus, string> = {
  active: '有效',
  expired: '已过期',
  revoked: '已撤销',
  exhausted: '次数用完',
}

/**
 * The backend's `active` flag is false for both revoked and expired links, so
 * the expiry timestamp decides which of the two the row is.
 * Order: revoked/expired first, then exhausted, otherwise the link is live.
 */
export function shareStatus(share: ShareInfo, now = Date.now()): ShareStatus {
  const expires = parseEpoch(share.expires_at)
  const expired = expires !== null && expires <= now

  if (!share.active) return expired ? 'expired' : 'revoked'
  if (share.max_downloads > 0 && share.download_count >= share.max_downloads) return 'exhausted'
  if (expired) return 'expired'
  return 'active'
}

export function shareDownloadsLabel(share: ShareInfo): string {
  return share.max_downloads > 0
    ? `${share.download_count} / ${share.max_downloads}`
    : `${share.download_count} / 不限`
}

/** Human countdown used in the create dialog and the public page. */
export function shareExpiryLabel(expiresAt: string | null): string {
  const expires = parseEpoch(expiresAt)
  if (expires === null) return '永久有效'

  const remaining = expires - Date.now()
  if (remaining <= 0) return '已过期'

  const day = 24 * 60 * 60 * 1000
  if (remaining >= day) return `${Math.ceil(remaining / day)} 天后过期`

  const hours = Math.ceil(remaining / (60 * 60 * 1000))
  if (hours >= 1) return `${hours} 小时后过期`
  return '1 小时内过期'
}

/** Absolute timestamp for tables; permanent links read better as 永久. */
export function shareExpiresAtLabel(expiresAt: string | null): string {
  return expiresAt ? formatDateTime(expiresAt) : '永久有效'
}

export function absoluteShareUrl(share: Pick<ShareInfo, 'url'>): string {
  return `${window.location.origin}${share.url}`
}
