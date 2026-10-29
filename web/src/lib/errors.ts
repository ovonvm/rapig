import { ApiError } from '../api/client'

/**
 * Server `message` strings are explicitly not stable (docs/08-api.md §4), so
 * the UI maps `code` to its own copy. Only an unknown code falls back to the
 * server message, and only when it is safe to show.
 */
const CODE_MESSAGES: Record<string, string> = {
  bad_request: '请求参数不合法',
  invalid_url: '链接格式不正确，请检查后重试',
  blocked_url: '该地址不允许访问（仅支持公网 HTTP/HTTPS 地址）',
  qbt_unavailable: '离线下载未配置 qBittorrent',
  invalid_name: '名称不合法，请检查后重试',
  unauthenticated: '登录状态已失效，请重新登录',
  invalid_credentials: '用户名或密码错误',
  csrf_failed: '请求校验失败，请刷新页面后重试',
  not_found: '资源不存在，可能已被删除',
  parent_not_found: '目标文件夹不存在，请刷新后重试',
  name_conflict: '同名文件或文件夹已存在，请改名或换位置',
  method_not_allowed: '请求方式不被支持',
  payload_too_large: '内容过大，超出服务器允许的大小',
  quota_exceeded: '存储空间不足，无法完成操作',
  insufficient_storage: '服务器磁盘空间不足，请稍后重试',
  too_many_attempts: '尝试次数过多，请稍后再试',
  checksum_mismatch: '文件校验失败，请重新上传',
  invalid_state: '当前状态无法执行该操作，请刷新后重试',
  invalid_target: '不能移动到自身或它的子文件夹里',
  stale_write: '页面内容已过期，请刷新后重试',
  share_exhausted: '分享下载次数已用完',
  internal: '服务器内部错误，请稍后重试',
  invalid_scope: '服务器内部错误，请稍后重试',
  frontend_not_built: '前端资源未构建',
  network_error: '无法连接服务器，请检查网络后重试',
  invalid_response: '服务器返回了无法识别的内容',
  unknown_error: '请求失败，请稍后重试',
}

const DEFAULT_MESSAGE = '操作失败，请稍后重试'

/**
 * `error.reason` narrows down codes whose meaning depends on context (M1
 * ruling: password rules reuse `bad_request`). Reasons are stable contract.
 */
const REASON_MESSAGES: Record<string, string> = {
  password_too_short: '新密码至少 8 个字符',
  password_reuse: '新密码不能与当前密码相同',
  offset_mismatch: '上传数据不连续，请重新上传',
  upload_expired: '上传已过期，请重新上传',
  // Share lifecycle (M3.2.1): each 409 shares `invalid_state`.
  not_revoked: '该分享未被撤销',
  share_active: '分享仍有效，请先撤销',
  share_expired: '分享已过期，无法恢复',
  root_not_shareable: '暂不支持分享根目录',
  // Search validation (M5.1): all 400 `bad_request`.
  query_required: '请输入搜索关键词',
  out_of_range: '搜索词或分页参数超出允许范围',
  invalid_type: '筛选类型不合法',
  // Offline downloads (M6.1): magnet source validation.
  invalid_magnet: '磁力链接格式不正确',
  // Torrent upload (M6.1b): qBittorrent `.torrent` source.
  invalid_torrent: '种子文件无法解析',
  torrent_too_large: '种子文件不能超过 20 MB',
  duplicate_torrent: 'qBittorrent 中已存在相同的种子任务，请先在 qBittorrent 中删除旧任务',
  // Registration (M5.2b): 403/409/429 carry these reasons.
  registration_disabled: '站点未开放注册',
  identifier_taken: '该用户名/邮箱/手机号已被使用',
  rate_limited: '尝试过于频繁，请稍后再试',
  root_admin_required: '仅 root 管理员可修改该设置',
  // Account deletion (M5.2c): 403 `forbidden`, 409 `invalid_state`, 400 `bad_request`.
  deletion_disabled: '站点未开放账号注销',
  deletion_pending: '该账号已申请注销，可在宽限期内联系管理员恢复',
  already_deleted: '该账号已在注销流程中',
  root_admin: 'root 管理员不能注销账号',
  username_reserved: '该用户名保留期内，暂不可用',
  // Storage policies (M5.3): a move/copy may not cross storage roots.
  cross_policy_operation: '不支持跨存储移动/复制',
  // Group spaces (P1): 400/409 validation reasons.
  group_perms_invalid: '组员权限至少需要包含「读取」',
  group_not_empty: '组空间非空（或仍有进行中的上传/分享），无法删除',
  last_group_manager: '不能移除或降级最后一个组管理员',
  cross_group_operation: '不支持跨组移动或复制',
  // Storage policy validation (M5.3 admin console): the server creates the
  // root and probes it, so these all mean "that path cannot be used".
  not_writable: '根路径无法创建或写入：请确认是绝对路径，且服务进程对该位置有写权限',
  symlink_root: '根路径不能是符号链接（请填真实目录）',
  path_overlap: '根路径与其它存储重叠：每个存储必须是独立目录',
  storage_in_use: '该存储下还有节点或文件，无法删除或修改根路径',
  default_policy: '默认存储不能停用或删除',
  last_policy: '至少需要保留一个存储',
}

/** `invalid_credentials` means "current password is wrong" on /auth/password. */
const PASSWORD_CODE_MESSAGES: Record<string, string> = {
  invalid_credentials: '当前密码不正确',
}

/** Display copy for any thrown value, driven by the error `code`. */
export function messageForError(cause: unknown, fallback = DEFAULT_MESSAGE): string {
  if (cause instanceof ApiError) {
    if (cause.code === 'too_many_attempts') {
      return `尝试次数过多，请 ${cause.retryAfter ?? 30} 秒后再试`
    }

    if (cause.reason) {
      const reasonMessage = REASON_MESSAGES[cause.reason]
      if (reasonMessage) return reasonMessage
    }

    const mapped = CODE_MESSAGES[cause.code]
    if (mapped) {
      // `bad_request` carries validation detail only the server knows (which
      // field, which rule); the generic copy would just hide it.
      if (cause.code === 'bad_request' && cause.message) return cause.message
      return mapped
    }

    // Unknown code: the server message is the only remaining hint.
    return cause.message || fallback
  }

  return fallback
}

/** The password form sees `invalid_credentials` with a different meaning. */
export function messageForPasswordChange(cause: unknown, fallback = '修改密码失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.reason) {
      const reasonMessage = REASON_MESSAGES[cause.reason]
      if (reasonMessage) return reasonMessage
    }

    const mapped = PASSWORD_CODE_MESSAGES[cause.code]
    if (mapped) return mapped
  }

  return messageForError(cause, fallback)
}

/**
 * Public share password form (M3.2): `invalid_credentials` means the share
 * password is wrong, and a rate-limited `too_many_attempts` must tell the
 * visitor how long to wait.
 */
export function messageForSharePassword(cause: unknown, fallback = '解锁失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.code === 'too_many_attempts') {
      return `尝试过于频繁，请 ${cause.retryAfter ?? 60} 秒后再试`
    }
    if (cause.code === 'invalid_credentials' || cause.status === 401) return '密码不正确'
  }

  return messageForError(cause, fallback)
}

/**
 * Reason for a dead public share. The contract deliberately hides whether a
 * token is unknown, expired, revoked or its node was deleted (all 404).
 */
export function messageForPublicShare(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.code === 'share_exhausted' || cause.status === 410) return '分享下载次数已用完'
    if (cause.code === 'not_found' || cause.status === 404 || cause.status === 403) {
      return '分享不存在或已失效'
    }
  }

  return messageForError(cause, '分享加载失败，请稍后重试')
}

/**
 * Share lifecycle actions (revoke/restore/purge/create) carry context in
 * `reason`; the lifecycle reasons read better than the generic `invalid_state`.
 */
export function messageForShareAction(cause: unknown, fallback = '操作失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.reason) {
      const reasonMessage = REASON_MESSAGES[cause.reason]
      if (reasonMessage) return reasonMessage
    }
  }

  return messageForError(cause, fallback)
}

/**
 * Registration form (M5.2b): 403 `registration_disabled`, 409 `identifier_taken`
 * and 429 `rate_limited` each need their own copy; validation errors keep the
 * server message because only it knows which identifier was rejected.
 */
export function messageForRegister(cause: unknown, fallback = '注册失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.code === 'too_many_attempts' || cause.reason === 'rate_limited') {
      return `尝试过于频繁，请 ${cause.retryAfter ?? 60} 秒后再试`
    }
    if (cause.code === 'registration_disabled' || cause.reason === 'registration_disabled') {
      return '站点未开放注册'
    }
    if (cause.reason === 'identifier_taken' || cause.code === 'name_conflict') {
      return '该用户名/邮箱/手机号已被使用'
    }
    if (cause.reason === 'password_too_short') return '密码至少需要 8 个字符'
    if (cause.code === 'invalid_value' || cause.reason === 'invalid_value') {
      return '输入内容不合法，请检查后重试'
    }
  }

  return messageForError(cause, fallback)
}

/** Stable `code` for programmatic decisions; null for non-API failures. */export function errorCodeOf(cause: unknown): string | null {
  return cause instanceof ApiError ? cause.code : null
}

/**
 * Account deletion request (M5.2c). `invalid_credentials` means the *current
 * password* is wrong here, so it needs its own copy.
 */
export function messageForDeletion(cause: unknown, fallback = '申请注销失败，请稍后重试'): string {
  if (cause instanceof ApiError) {
    if (cause.code === 'invalid_credentials' || cause.status === 401) {
      return '当前密码不正确'
    }
  }

  return messageForError(cause, fallback)
}

/** Anonymous cancel page: any validation/api failure means the link is dead. */
export function messageForCancelDeletion(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (
      cause.code === 'bad_request' ||
      cause.code === 'not_found' ||
      cause.code === 'invalid_state'
    ) {
      return '链接无效或已过期'
    }
  }

  return messageForError(cause, '撤销失败，请稍后重试')
}

/** 409 `conflict` from a move/copy that would cross storage roots (M5.3). */
export function isCrossPolicyError(cause: unknown): boolean {
  return cause instanceof ApiError && cause.reason === 'cross_policy_operation'
}

export function isUnauthenticated(cause: unknown): boolean {
  return cause instanceof ApiError && (cause.code === 'unauthenticated' || cause.status === 401)
}
