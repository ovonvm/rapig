import { ApiError } from '../api/client'
import { formatBytes } from './format'

/** The console speaks decimal GB (1 GB = 1 000 000 000 bytes). */
export const GB_BYTES = 1_000_000_000

const ROLE_LABELS: Record<string, string> = {
  admin: '管理员',
  user: '成员',
}

const STATUS_LABELS: Record<string, string> = {
  active: '正常',
  disabled: '已禁用',
}

const RESULT_LABELS: Record<string, string> = {
  ok: '成功',
  success: '成功',
  denied: '拒绝',
  failure: '失败',
  error: '失败',
}

/** Only a handful of actions are worth translating; anything else is shown raw. */
const ACTION_LABELS: Record<string, string> = {
  login: '登录',
  login_failed: '登录失败',
  logout: '登出',
  password_change: '修改密码',
  password_reset: '重置密码',
  user_create: '创建用户',
  user_update: '更新用户',
  user_disable: '禁用用户',
  user_enable: '启用用户',
  user_delete: '删除用户',
  settings_update: '更新设置',
  share_create: '创建分享',
  share_revoke: '撤销分享',
  upload: '上传文件',
  download: '下载文件',
  node_delete: '删除文件',
  node_restore: '恢复文件',
  node_purge: '彻底删除',
}

const REASON_MESSAGES: Record<string, string> = {
  self_target: '不能对自己执行该操作',
  last_admin: '至少需要保留一个可用管理员',
  invalid_role: '角色不合法',
  invalid_value: '输入内容不合法，请检查后重试',
  password_too_short: '密码至少需要 8 个字符',
  name_conflict: '用户名或邮箱已被使用',
  invalid_id: '目标用户不存在',
  // Account deletion (M5.2c).
  root_admin: 'root 管理员不能执行该操作',
  already_deleted: '该账号已在注销流程中',
  deletion_pending: '该账号已申请注销，可在宽限期内恢复',
}

/**
 * One row of the storage admin page (M5.3). `used_bytes` counts blobs that live
 * under this root; `node_count`/`file_count` gate the editable root path.
 */
export interface StoragePolicyInfo {
  id: number
  name: string
  type: string
  root_path: string
  enabled: boolean
  is_default: boolean
  used_bytes: number
  node_count: number
  file_count: number
  created_at: string
  updated_at: string
}

/** Storage-specific reasons; `name_conflict`/`invalid_state` mean something else here. */
const STORAGE_REASON_MESSAGES: Record<string, string> = {
  storage_in_use: '该存储下还有节点或文件，不能删除',
  default_policy: '默认存储不能删除，请先把其它存储设为默认',
  last_policy: '至少要保留一个存储，不能删除最后一个',
  cross_policy_operation: '不支持跨存储移动/复制',
  name_conflict: '存储名称已存在，请更换',
  invalid_path: '根路径必须是绝对路径',
  path_not_found: '根路径不存在，请检查后重试',
  path_not_writable: '根路径不可写，请检查权限后重试',
  path_overlap: '该路径与其它存储重叠，请更换',
}

export function roleLabel(role: string): string {
  return ROLE_LABELS[role] ?? role
}

export function statusLabel(status: string): string {
  return STATUS_LABELS[status] ?? status
}

/**
 * Badge copy for an account inside the deletion grace period (M5.2c):
 * "注销中（剩余 3 天）" with a friendlier sub-day wording.
 */
export function deletionRemainingLabel(purgeAfter: string | null | undefined, now = Date.now()): string | null {
  if (!purgeAfter) return null
  const stamp = Date.parse(purgeAfter)
  if (!Number.isFinite(stamp)) return '注销中'
  const remaining = stamp - now
  if (remaining <= 0) return '注销中（待删除）'
  if (remaining < 86_400_000) return '注销中（不足 1 天）'
  return `注销中（剩余 ${Math.ceil(remaining / 86_400_000)} 天）`
}

export function auditResultLabel(result: string): string {
  return RESULT_LABELS[result] ?? result
}

export function auditActionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

/** `0` means "unlimited" everywhere in the admin console. */
export function quotaText(bytes: number): string {
  return bytes > 0 ? formatBytes(bytes) : '不限'
}

export function usageText(usedBytes: number, quotaBytes: number): string {
  return `${formatBytes(usedBytes)} / ${quotaText(quotaBytes)}`
}

export function bytesToGbText(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0'
  const gb = bytes / GB_BYTES
  const rounded = Math.round(gb * 100) / 100
  return String(rounded)
}

/**
 * Parses the GB input of the console forms. Returns `null` for anything that
 * is not a non-negative number, so callers can show a field error.
 */
export function parseGbInput(raw: string): number | null {
  const text = raw.trim()
  if (!text) return null
  if (!/^\d+(\.\d{1,6})?$/.test(text)) return null
  const value = Number(text)
  if (!Number.isFinite(value)) return null
  const bytes = Math.round(value * GB_BYTES)
  return Number.isSafeInteger(bytes) ? bytes : null
}

/** Maps admin API failures to Chinese copy, by reason first and then by code. */
export function adminErrorMessage(cause: unknown, fallback = '操作失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.reason) {
      const byReason = REASON_MESSAGES[cause.reason]
      if (byReason) return byReason
    }
    switch (cause.code) {
      case 'not_found':
        return '没有权限或资源不存在'
      case 'forbidden':
        return '没有权限执行该操作'
      case 'invalid_state':
        return '当前状态下无法执行该操作，请刷新后重试'
      case 'name_conflict':
        return '用户名已存在，请更换'
      case 'csrf_failed':
      case 'unauthenticated':
        return '登录状态已失效，请重新登录'
      default:
        return cause.message || fallback
    }
  }
  return fallback
}

/**
 * Display copy for storage failures. Reasons win over codes because the console
 * reuses `name_conflict`/`invalid_state` with a different meaning here.
 */
export function storageErrorMessage(cause: unknown, fallback = '操作失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.reason) {
      const byReason = STORAGE_REASON_MESSAGES[cause.reason]
      if (byReason) return byReason
    }
    switch (cause.code) {
      case 'not_found':
        return '存储不存在，可能已被删除'
      case 'forbidden':
        return '没有权限执行该操作'
      case 'name_conflict':
        return '存储名称已存在，请更换'
      case 'invalid_state':
        return '当前状态下无法执行该操作，请刷新后重试'
      case 'csrf_failed':
      case 'unauthenticated':
        return '登录状态已失效，请重新登录'
      default:
        return cause.message || fallback
    }
  }
  return fallback
}

/** `datetime-local` value → Unix seconds, `null` when empty/invalid. */
export function localInputToUnix(value: string): number | null {
  if (!value) return null
  const stamp = new Date(value).getTime()
  if (!Number.isFinite(stamp)) return null
  return Math.floor(stamp / 1000)
}

/** Unix seconds → `datetime-local` value in local time. */
export function unixToLocalInput(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return ''
  const date = new Date(seconds * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}
