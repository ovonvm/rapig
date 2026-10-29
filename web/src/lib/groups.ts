import type { GroupPermission, GroupRole } from '../api/client'
import { errorCodeOf, messageForError } from './errors'

export const GROUP_NAME_MAX = 64

/** The six member actions, in the order the console shows them. */
export const GROUP_PERMISSIONS: readonly { value: GroupPermission; label: string; hint: string }[] = [
  { value: 'read', label: '查看', hint: '浏览组空间、下载文件' },
  { value: 'create', label: '新建', hint: '新建文件夹、复制文件' },
  { value: 'upload', label: '上传', hint: '把文件上传到组空间' },
  { value: 'write', label: '编辑', hint: '改名、移动组内文件' },
  { value: 'delete', label: '删除', hint: '永久删除组内文件（无回收站）' },
  { value: 'share', label: '分享', hint: '创建、撤销组内文件的分享链接' },
]

const ALL_PERMISSIONS = GROUP_PERMISSIONS.map((item) => item.value)

export function groupRoleLabel(role: GroupRole): string {
  return role === 'manager' ? '管理员' : '组员'
}

export function permissionLabel(permission: GroupPermission): string {
  return GROUP_PERMISSIONS.find((item) => item.value === permission)?.label ?? permission
}

export function permissionLabels(permissions: readonly GroupPermission[]): string[] {
  return GROUP_PERMISSIONS.filter((item) => permissions.includes(item.value)).map((item) => item.label)
}

/**
 * The server requires `read` and rejects unknown entries; keep the console
 * payload in the same canonical shape (read first, no duplicates).
 */
export function normalizePermissions(permissions: readonly GroupPermission[]): GroupPermission[] {
  const unique = ALL_PERMISSIONS.filter((permission) => permissions.includes(permission))
  return unique.includes('read') ? unique : ['read', ...unique]
}

export function validateGroupName(raw: string): string | null {
  const name = raw.trim()
  if (!name) return '请输入组名称'
  if (Array.from(name).length > GROUP_NAME_MAX) return `组名称不能超过 ${GROUP_NAME_MAX} 个字符`
  if (/[\u0000-\u001f\u007f]/.test(name)) return '组名称不能包含不可见字符'
  return null
}

/** Managers ignore `member_perms`; members must hold the permission. */
export function canGroupAction(
  role: GroupRole,
  permissions: readonly GroupPermission[],
  action: GroupPermission,
): boolean {
  if (role === 'manager') return true
  return permissions.includes(action)
}

/**
 * Group-space mutations answer 403 `forbidden` when the role lacks the
 * permission, and 404 when the caller is not even a member.
 */
export function groupErrorMessage(cause: unknown, fallback = '操作失败，请稍后重试'): string {
  const code = errorCodeOf(cause)
  if (code === 'forbidden') return '当前角色没有执行该操作的权限'
  if (code === 'not_found') return '组不存在，或你没有访问权限'
  if (code === 'cross_group_operation') return '不支持跨组移动或复制'
  return messageForError(cause, fallback)
}
