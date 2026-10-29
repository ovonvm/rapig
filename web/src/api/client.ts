/**
 * The only place that talks to the Rapig HTTP API.
 * Endpoint shapes follow the frozen M0/M1 contract (docs/08-api.md,
 * docs/09-m1-plan.md §6); components and stores consume the typed helpers
 * below, never fetch directly.
 */
import { parseRetryAfter } from '../lib/time'
import type { StoragePolicyInfo } from '../lib/admin'
import type { StoragePolicyOption } from '../lib/files'

export interface User {
  id: number
  username: string
  role: string
  used_bytes: number
  quota_bytes: number
  /** Optional login identifiers (M5.2b); /auth/me returns them in full. */
  email?: string
  phone?: string
  /** Only the root administrator may change the registration switch. */
  is_root?: boolean
  /** Delegated ability to create and manage one's own groups (P1). */
  can_create_groups?: boolean
}

export type NodeKind = 'dir' | 'file'

export interface NodeItem {
  id: number
  parent_id: number
  kind: NodeKind
  name: string
  size: number
  /** Optimistic-locking token (M2): echo it back on PATCH, or get 409 stale_write. */
  revision: string
  created_at: string
  updated_at: string
  /** Present (non-null) when the node lives inside a group space (P1). */
  group_id?: number | null
}

export interface NodePage {
  items: NodeItem[]
  total: number
  limit: number
  offset: number
}

/** One recycle-bin entry: a node plus where it came from and when it expires. */
export interface TrashItem extends NodeItem {
  /** Parent at the moment of deletion; 0 means the drive root. */
  original_parent_id: number
  trashed_at: string
  expires_at: string
  /** Whole days left before automatic purge; 0 means less than a day. */
  remaining_days: number
}

export interface TrashPage {
  items: TrashItem[]
  total: number
  limit: number
  offset: number
}

export interface HealthStatus {
  status: string
  db: string
  version: string
}

export interface CreateNodeInput {
  parent: number
  name: string
  kind: NodeKind
  /** Root folders only (M5.3): the storage root the new directory lives on. */
  storage_policy_id?: number
}

export interface ListNodesParams {
  parent: number
  limit?: number
  offset?: number
}

/** Optional overrides for a copy; both default to "same folder, - 副本". */
export interface CopyNodeInput {
  parent?: number
  name?: string
}

export interface UpdateNodeInput {
  /** Rename; omitted when only moving. */
  name?: string
  /** New parent directory id; 0 means the drive root. Omitted when only renaming. */
  parent?: number
  /** The `revision` the client last read for this node. */
  revision: string
}

export interface UploadInit {
  upload_id: string
  part_size: number
  parts_total: number
  expires_at: string
  deduped_node_id?: number
}

export interface InitUploadInput {
  parent: number
  name: string
  size: number
  sha256?: string
}

export interface PasswordChangeInput {
  current_password: string
  new_password: string
}

/* --- shares (M3.2) ------------------------------------------------------- */

/**
 * `multi` marks a merged share (2+ nodes behind one token); its `node_id` is
 * the first member and `item_count` holds the member count.
 */
export type ShareKind = NodeKind | 'multi'

/** One share link as the owner sees it (`GET /api/v1/shares`). */
export interface ShareInfo {
  /** Internal id (TEXT); revoking uses this, never the token. */
  id: string
  token: string
  /** Server-provided path, e.g. `/s/<token>`. */
  url: string
  node_id: number
  name: string
  kind: ShareKind
  /** Present on merged shares only. */
  item_count?: number
  has_password: boolean
  /** RFC3339, or null for a permanent link. */
  expires_at: string | null
  /** 0 means unlimited. */
  max_downloads: number
  download_count: number
  visits_count: number
  last_access_at: string | null
  created_at: string
  /** false once revoked. */
  active: boolean
}

export interface SharePage {
  items: ShareInfo[]
  total: number
}

export type ShareStatusFilter = 'all' | 'active' | 'revoked' | 'expired'

/** Exactly one of `node_id` (single) or `node_ids` (2+ merged share). */
export interface CreateShareInput {
  node_id?: number
  node_ids?: number[]
  /** Omitted or empty for a link without a password. */
  password?: string
  /** 3600 | 86400 | 604800 | 2592000 | null (permanent). */
  expires_in?: number | null
  /** 0–1000000; 0 means unlimited. */
  max_downloads?: number
}

/**
 * Anonymous metadata for `/s/:token`: the locked form omits `name`/`size` so a
 * password-protected share does not leak the file name before unlocking.
 */
export interface PublicShareMeta {
  locked?: boolean
  node_id: number
  name?: string
  kind: ShareKind
  /** Present on merged shares only. */
  item_count?: number
  size?: number
  expires_at: string | null
  has_password: boolean
  /** null means unlimited; 0 means used up. */
  downloads_left: number | null
}

/** One entry inside a directory share. */
export interface PublicNode {
  id: number
  name: string
  kind: NodeKind
  size: number
  updated_at: string
}

export interface PublicNodePage {
  items: PublicNode[]
  total: number
  limit: number
  offset: number
  /** Current level; `id` 0 means the merged share root. */
  parent?: { id: number; name: string }
}

const DEFAULT_ERROR_MESSAGE = '请求失败，请稍后重试'
const INVALID_RESPONSE_MESSAGE = '服务器返回了无法识别的内容'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId: string | null
  readonly retryAfter: number | null
  /** Optional machine-readable detail from `error.reason` (e.g. `password_reuse`). */
  readonly reason: string | null

  constructor(
    status: number,
    code: string,
    message: string,
    requestId: string | null = null,
    retryAfter: number | null = null,
    reason: string | null = null,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
    this.retryAfter = retryAfter
    this.reason = reason
  }
}

type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

interface RequestOptions {
  method?: HttpMethod
  body?: unknown
  /** Binary payload (upload part); takes precedence over `body`. */
  rawBody?: Blob | ArrayBuffer | FormData
  headers?: Record<string, string>
  signal?: AbortSignal
}

interface ErrorDetail {
  code: string
  message: string | undefined
  requestId: string | null
  reason: string | null
}

/* --- transport ----------------------------------------------------------- */

function readCookie(name: string): string | null {
  const prefix = `${name}=`
  for (const part of document.cookie.split(';')) {
    const entry = part.trim()
    if (entry.startsWith(prefix)) return decodeURIComponent(entry.slice(prefix.length))
  }
  return null
}

function isMutating(method: string): boolean {
  return method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS'
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return null
  }
}

/** Unpack `{"error":{"code","message","request_id","reason"}}` without trusting the body. */
function extractError(data: unknown): ErrorDetail {
  const fallback: ErrorDetail = { code: 'unknown_error', message: undefined, requestId: null, reason: null }
  if (typeof data !== 'object' || data === null) return fallback

  const error = (data as { error?: unknown }).error
  if (typeof error !== 'object' || error === null) return fallback

  const record = error as Record<string, unknown>
  const message = typeof record.message === 'string' ? record.message.trim() : ''
  const reason = typeof record.reason === 'string' ? record.reason.trim() : ''

  return {
    code: typeof record.code === 'string' && record.code ? record.code : 'unknown_error',
    message: message || undefined,
    requestId: typeof record.request_id === 'string' ? record.request_id : null,
    reason: reason || null,
  }
}

/** Turn a non-ok response into the typed error every caller expects. */
async function readError(response: Response): Promise<ApiError> {
  const text = await response.text()
  const detail = extractError(text ? parseJson(text) : null)
  return new ApiError(
    response.status,
    detail.code,
    detail.message ?? DEFAULT_ERROR_MESSAGE,
    detail.requestId,
    parseRetryAfter(response.headers.get('Retry-After')),
    detail.reason,
  )
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET'
  const headers: Record<string, string> = { Accept: 'application/json', ...options.headers }

  let body: BodyInit | undefined
  if (options.rawBody !== undefined) {
    body = options.rawBody
  } else if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(options.body)
  }

  if (isMutating(method)) {
    const csrf = readCookie('rapig_csrf')
    if (csrf) headers['X-CSRF-Token'] = csrf
  }

  let response: Response
  try {
    response = await fetch(path, {
      method,
      headers,
      body,
      credentials: 'same-origin',
      signal: options.signal,
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') {
      throw new ApiError(0, 'aborted', '请求已取消')
    }
    throw new ApiError(0, 'network_error', '无法连接服务器，请检查网络后重试')
  }

  if (response.status === 204) return undefined as T

  if (!response.ok) throw await readError(response)

  const text = await response.text()
  const data = text ? parseJson(text) : null

  if (data === null) throw new ApiError(response.status, 'invalid_response', INVALID_RESPONSE_MESSAGE)

  return data as T
}

/* --- health / auth ------------------------------------------------------- */

export function fetchHealth(): Promise<HealthStatus> {
  return request<HealthStatus>('/health')
}

export async function login(username: string, password: string): Promise<User> {
  // Login itself is CSRF protected; a GET first hands out the cookie on a cold visit.
  if (!readCookie('rapig_csrf')) {
    try {
      await request<HealthStatus>('/health')
    } catch {
      // Fall through: the POST below surfaces the real problem.
    }
  }

  const data = await request<{ user?: User }>('/api/v1/auth/login', {
    method: 'POST',
    body: { username, password },
  })
  if (!data || typeof data.user !== 'object' || data.user === null) {
    throw new ApiError(200, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return data.user
}

export async function fetchMe(): Promise<User> {
  const data = await request<{ user?: User }>('/api/v1/auth/me')
  if (!data || typeof data.user !== 'object' || data.user === null) {
    throw new ApiError(200, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return data.user
}

export interface RegistrationStatus {
  enabled: boolean
}

/** Public: whether this instance accepts self-registration (M5.2b). */
export function fetchRegistrationEnabled(): Promise<boolean> {
  return request<{ enabled?: unknown }>('/api/v1/auth/registration').then((data) => {
    if (!data || typeof data.enabled !== 'boolean') {
      throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
    }
    return data.enabled
  })
}

export interface RegisterInput {
  username?: string
  email?: string
  phone?: string
  password: string
}

/** Public: create an account; the server does not sign the new user in. */
export async function registerAccount(input: RegisterInput): Promise<User> {
  // Registration is a POST, so make sure a CSRF cookie exists on a cold visit.
  if (!readCookie('rapig_csrf')) {
    try {
      await request<HealthStatus>('/health')
    } catch {
      // Fall through: the POST below surfaces the real problem.
    }
  }

  const data = await request<{ user?: User }>('/api/v1/auth/register', {
    method: 'POST',
    body: input,
  })
  if (!data || typeof data.user !== 'object' || data.user === null) {
    throw new ApiError(201, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return data.user
}

export function logout(): Promise<void> {
  return request<void>('/api/v1/auth/logout', { method: 'POST' })
}

/** Changing the password makes the server drop every other session. */
export function changePassword(input: PasswordChangeInput): Promise<void> {
  return request<void>('/api/v1/auth/password', { method: 'POST', body: input })
}

/* --- account deletion (M5.2c) -------------------------------------------- */

export interface AccountDeletionTicket {
  /** One-time token embedded in the cancel link; shown exactly once. */
  cancel_token: string
  /** RFC3339 moment the account becomes unrecoverable. */
  purge_at: string
}

export async function requestAccountDeletion(password: string): Promise<AccountDeletionTicket> {
  const data = await request<AccountDeletionTicket>('/api/v1/auth/deletion/request', {
    method: 'POST',
    body: { password },
  })
  if (!data || typeof data.cancel_token !== 'string' || typeof data.purge_at !== 'string') {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return data
}

export async function cancelAccountDeletion(token: string): Promise<void> {
  // Anonymous POST: a cold visit needs a GET first to receive `rapig_csrf`.
  if (!readCookie('rapig_csrf')) {
    try {
      await request<HealthStatus>('/health')
    } catch {
      // Fall through: the POST below surfaces the real problem.
    }
  }
  const data = await request<{ restored?: boolean }>('/api/v1/auth/deletion/cancel', {
    method: 'POST',
    body: { token },
  })
  if (!data || data.restored !== true) {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
}

/* --- nodes --------------------------------------------------------------- */

export function listNodes({ parent, limit = 50, offset = 0 }: ListNodesParams): Promise<NodePage> {
  const query = new URLSearchParams({
    parent: String(parent),
    limit: String(limit),
    offset: String(offset),
  })
  return request<NodePage>(`/api/v1/nodes?${query.toString()}`)
}

export function createNode(input: CreateNodeInput): Promise<NodeItem> {
  return request<NodeItem>('/api/v1/nodes', { method: 'POST', body: input })
}

/** Download links go through the browser so cookies and Range work naturally. */
export function downloadUrl(id: number): string {
  return `/api/v1/nodes/${id}/content`
}

/** Rename and/or move one node; `revision` must be the value last read. */
export function updateNode(id: number, input: UpdateNodeInput): Promise<NodeItem> {
  return request<NodeItem>(`/api/v1/nodes/${id}`, { method: 'PATCH', body: input })
}

/**
 * Soft delete into the recycle bin (returns the trashed node); `purge` deletes
 * permanently (204, no body).
 */
export async function deleteNode(id: number, purge = false): Promise<NodeItem | null> {
  const query = purge ? '?purge=1' : ''
  if (purge) {
    await request<void>(`/api/v1/nodes/${id}${query}`, { method: 'DELETE' })
    return null
  }
  return request<NodeItem>(`/api/v1/nodes/${id}${query}`, { method: 'DELETE' })
}

/** Bring a trashed node back; the server may rename it on conflict. */
export function restoreNode(id: number): Promise<NodeItem> {
  return request<NodeItem>(`/api/v1/nodes/${id}/restore`, { method: 'POST', body: {} })
}

/** Copy one node; defaults are the same parent and 「原名 - 副本」. */
export function copyNode(id: number, input: CopyNodeInput = {}): Promise<NodeItem> {
  return request<NodeItem>(`/api/v1/nodes/${id}/copy`, { method: 'POST', body: input })
}

export function listTrash({ limit = 50, offset = 0 }: { limit?: number; offset?: number } = {}): Promise<TrashPage> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  return request<TrashPage>(`/api/v1/trash?${query.toString()}`)
}

/** Permanently delete every recycle-bin entry; resolves with the purged count. */
export async function emptyTrash(): Promise<number> {
  const data = await request<{ purged?: number }>('/api/v1/trash/empty', { method: 'POST', body: {} })
  return typeof data?.purged === 'number' ? data.purged : 0
}

/* --- uploads ------------------------------------------------------------- */

export async function initUpload(input: InitUploadInput): Promise<UploadInit> {
  const data = await request<UploadInit>('/api/v1/uploads', { method: 'POST', body: input })
  if (!data || typeof data.upload_id !== 'string' || !data.upload_id) {
    throw new ApiError(201, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return data
}

/** `start`/`end` are inclusive byte offsets; the server validates them. */
export function putUploadPart(
  uploadId: string,
  partNo: number,
  chunk: Blob,
  start: number,
  end: number,
  total: number,
  signal?: AbortSignal,
): Promise<void> {
  return request<void>(`/api/v1/uploads/${encodeURIComponent(uploadId)}/parts/${partNo}`, {
    method: 'PUT',
    rawBody: chunk,
    headers: {
      'Content-Type': 'application/octet-stream',
      'Content-Range': `bytes ${start}-${end}/${total}`,
    },
    signal,
  })
}

export function completeUpload(uploadId: string): Promise<NodeItem> {
  // `parts` is optional in the contract; the server hashes parts itself.
  return request<NodeItem>(`/api/v1/uploads/${encodeURIComponent(uploadId)}/complete`, {
    method: 'POST',
    body: {},
  })
}

export function cancelUpload(uploadId: string): Promise<void> {
  return request<void>(`/api/v1/uploads/${encodeURIComponent(uploadId)}`, { method: 'DELETE' })
}

/* --- shares: owner ------------------------------------------------------- */

function pickShare(data: unknown, invalidStatus = 200): ShareInfo {
  const share = (data as { share?: unknown } | null)?.share
  if (typeof share !== 'object' || share === null) {
    throw new ApiError(invalidStatus, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  const record = share as Record<string, unknown>
  if (typeof record.id !== 'string' || typeof record.token !== 'string') {
    throw new ApiError(invalidStatus, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return share as ShareInfo
}

/** Create a share link for one node; resolves with the owner view of it. */
export async function createShare(input: CreateShareInput): Promise<ShareInfo> {
  const data = await request<unknown>('/api/v1/shares', { method: 'POST', body: input })
  return pickShare(data, 201)
}

export function listShares({
  status = 'all',
  limit = 50,
  offset = 0,
}: { status?: ShareStatusFilter; limit?: number; offset?: number } = {}): Promise<SharePage> {
  const query = new URLSearchParams({ status, limit: String(limit), offset: String(offset) })
  return request<SharePage>(`/api/v1/shares?${query.toString()}`)
}

/** Idempotent: revoking an unknown or already revoked share still returns 204. */
export function revokeShare(id: string): Promise<void> {
  return request<void>(`/api/v1/shares/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/**
 * Permanently delete a revoked/expired share record. The server rejects active
 * shares with 409 `invalid_state` + reason `share_active`.
 */
export function purgeShare(id: string): Promise<void> {
  return request<void>(`/api/v1/shares/${encodeURIComponent(id)}?purge=1`, { method: 'DELETE' })
}

/** Re-enable a revoked share; the token and counters are kept. */
export async function restoreShare(id: string): Promise<ShareInfo> {
  const data = await request<unknown>(`/api/v1/shares/${encodeURIComponent(id)}/restore`, {
    method: 'POST',
    body: {},
  })
  return pickShare(data)
}

/* --- shares: public ------------------------------------------------------ */

/** Anonymous metadata; throws 404/410 for dead or exhausted links. */
export async function getPublicShare(token: string): Promise<PublicShareMeta> {
  const data = await request<unknown>(`/api/v1/public/shares/${encodeURIComponent(token)}`)
  const share = (data as { share?: unknown } | null)?.share
  if (typeof share !== 'object' || share === null) {
    throw new ApiError(200, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return share as PublicShareMeta
}

/**
 * Exchange the share password for the short-lived ticket cookie. The verify
 * POST is CSRF protected, so a cold anonymous visit needs a GET first to
 * receive `rapig_csrf` (the metadata GET normally provides it).
 */
export async function unlockShare(token: string, password: string): Promise<void> {
  if (!readCookie('rapig_csrf')) {
    try {
      await request<HealthStatus>('/health')
    } catch {
      // Fall through: the POST below surfaces the real problem.
    }
  }
  await request<void>(`/api/v1/public/shares/${encodeURIComponent(token)}/verify`, {
    method: 'POST',
    body: { password },
  })
}

/** Directory share listing; omit `parent` for the share root. */
export function listPublicNodes(
  token: string,
  { parent, limit = 50, offset = 0 }: { parent?: number; limit?: number; offset?: number } = {},
): Promise<PublicNodePage> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  if (parent !== undefined) query.set('parent', String(parent))
  return request<PublicNodePage>(
    `/api/v1/public/shares/${encodeURIComponent(token)}/nodes?${query.toString()}`,
  )
}

/** Direct download link for a file inside a share (cookie/ticket auth applies). */
export function publicDownloadUrl(token: string, nodeId: number): string {
  return `/api/v1/public/shares/${encodeURIComponent(token)}/nodes/${nodeId}/content`
}

/* --- offline downloads (transfers) --------------------------------------- */

export type TransferState = 'pending' | 'running' | 'paused' | 'completed' | 'failed' | 'canceled'

/** `http` is a server-side URL fetch; `qb` is handed to qBittorrent. */
export type TransferKind = 'http' | 'qb'

/**
 * One server-side fetch into the drive. `size` is 0 until the size is known;
 * `node_id` is set once the download has been saved. `kind` is optional on the
 * wire for older tasks, so `pickTransfer` normalises it to `http`.
 */
export interface TransferItem {
  id: string
  parent_id: number
  name: string
  kind: TransferKind
  source_url: string
  size: number
  received_bytes: number
  state: TransferState
  error: string | null
  node_id: number | null
  created_at: string
  updated_at: string
  started_at: string | null
  finished_at: string | null
}

export interface TransferPage {
  items: TransferItem[]
  total: number
  limit: number
  offset: number
}

export interface CreateTransferInput {
  /** 0 = drive root. */
  parent: number
  name?: string
  /** HTTP(S) source; required for `kind: 'http'`. */
  url?: string
  /** Defaults to `http` server-side. */
  kind?: TransferKind
  /** `magnet:?…` source; required for `kind: 'qb'`. */
  magnet?: string
}

function pickTransfer(data: unknown, invalidStatus = 200): TransferItem {
  const transfer = (data as { transfer?: unknown } | null)?.transfer
  if (typeof transfer !== 'object' || transfer === null) {
    throw new ApiError(invalidStatus, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  const record = transfer as Record<string, unknown>
  if (typeof record.id !== 'string' || typeof record.source_url !== 'string') {
    throw new ApiError(invalidStatus, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  // Older tasks have no `kind`; treat them as HTTP fetches.
  return { ...(transfer as TransferItem), kind: record.kind === 'qb' ? 'qb' : 'http' }
}

/** Magnet support requires qBittorrent configured on the server. */
export async function getQBStatus(): Promise<boolean> {
  const data = await request<unknown>('/api/v1/transfers/qb/status')
  const enabled = (data as { enabled?: unknown } | null)?.enabled
  if (typeof enabled !== 'boolean') {
    throw new ApiError(200, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return enabled
}

/**
 * Upload a `.torrent` file for qBittorrent. The browser sets the multipart
 * boundary, so no Content-Type header may be passed here.
 */
export async function createTransferTorrent(
  file: File,
  input: { parent?: number; name?: string } = {},
): Promise<TransferItem> {
  const form = new FormData()
  form.append('file', file, file.name)
  form.append('parent', String(input.parent ?? 0))
  const name = input.name?.trim()
  if (name) form.append('name', name)

  const data = await request<unknown>('/api/v1/transfers/torrent', { method: 'POST', rawBody: form })
  return pickTransfer(data, 201)
}

/** Server-side downloads; `status` empty means every state. */
export function listTransfers({
  status = '',
  limit = 50,
  offset = 0,
}: { status?: TransferState | ''; limit?: number; offset?: number } = {}): Promise<TransferPage> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  if (status) query.set('status', status)
  return request<TransferPage>(`/api/v1/transfers?${query.toString()}`)
}

/** Queue a URL fetch into `parent`; the server derives the name from the URL. */
export async function createTransfer(input: CreateTransferInput): Promise<TransferItem> {
  const data = await request<unknown>('/api/v1/transfers', { method: 'POST', body: input })
  return pickTransfer(data, 201)
}

export function pauseTransfer(id: string): Promise<void> {
  return request<void>(`/api/v1/transfers/${id}/pause`, { method: 'POST', body: {} })
}

export function resumeTransfer(id: string): Promise<void> {
  return request<void>(`/api/v1/transfers/${id}/resume`, { method: 'POST', body: {} })
}

/** Cancel and delete the transfer row; already saved files are kept. */
export function deleteTransfer(id: string): Promise<void> {
  return request<void>(`/api/v1/transfers/${id}`, { method: 'DELETE' })
}

/* --- search (M5.1) ------------------------------------------------------- */

/** Empty means "both kinds"; the server rejects anything else. */
export type SearchType = '' | 'file' | 'dir'

export interface SearchNodesParams {
  /** Trimmed, non-empty, at most 100 runes; the store guards this. */
  q: string
  type?: SearchType
  /** `true` sorts by update time instead of relevance. */
  recent?: boolean
  limit?: number
  offset?: number
  /** Lets the search store discard a superseded request. */
  signal?: AbortSignal
}

/**
 * Filename search over the caller's own live nodes. Result rows are plain
 * `NodeItem`s, so reuse the file-list rendering; there is no path field, so
 * "open containing folder" uses `parent_id` (0 = drive root).
 */
export function searchNodes({
  q,
  type = '',
  recent = false,
  limit = 50,
  offset = 0,
  signal,
}: SearchNodesParams): Promise<NodePage> {
  const query = new URLSearchParams({
    q,
    limit: String(limit),
    offset: String(offset),
  })
  if (type) query.set('type', type)
  if (recent) query.set('recent', '1')
  return request<NodePage>(`/api/v1/nodes/search?${query.toString()}`, { signal })
}

/* --- groups: shared spaces (P1) ----------------------------------------- */

export type GroupRole = 'manager' | 'member'
export type GroupPermission = 'read' | 'create' | 'upload' | 'write' | 'delete' | 'share'

/** A group the signed-in user belongs to: the `/groups/:id` scope. */
export interface MyGroup {
  id: number
  name: string
  role: GroupRole
  /** Root node id of the group space; all group listings hang off it. */
  root_id: number
  member_perms: GroupPermission[]
}

/** Console view of a group (admin / group creator). */
export interface Group {
  id: number
  name: string
  member_perms: GroupPermission[]
  created_by: number | null
  member_count: number
  node_count: number
  created_at: string
}

export interface CreatedGroup {
  group: Group
  root_id: number
}

export interface CreateGroupInput {
  name: string
  member_perms?: GroupPermission[]
}

export interface AdminCreateGroupInput extends CreateGroupInput {
  /** Optional initial manager; defaults to the creator. */
  manager_user_id?: number
}

export interface UpdateGroupInput {
  name?: string
  member_perms?: GroupPermission[]
}

export interface GroupMember {
  user_id: number
  username: string
  display_name: string
  role: GroupRole
}

export interface AddGroupMemberInput {
  user_id: number
  role: GroupRole
}

const GROUP_PERMISSION_SET: readonly GroupPermission[] = ['read', 'create', 'upload', 'write', 'delete', 'share']

function permissionsOf(value: unknown): GroupPermission[] {
  if (!Array.isArray(value)) return ['read']
  const perms = value.filter((item): item is GroupPermission =>
    typeof item === 'string' && (GROUP_PERMISSION_SET as readonly string[]).includes(item),
  )
  return perms.length > 0 ? perms : ['read']
}

/** Both `{group, root_id}` and a bare group object are accepted. */
function groupOf(data: unknown): Group {
  const envelope = data as { group?: unknown } | null
  const candidate = (envelope && envelope.group ? envelope.group : data) as Partial<Group> | null
  if (!candidate || typeof candidate.id !== 'number' || typeof candidate.name !== 'string') {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return {
    id: candidate.id,
    name: candidate.name,
    member_perms: permissionsOf(candidate.member_perms),
    created_by: typeof candidate.created_by === 'number' ? candidate.created_by : null,
    member_count: typeof candidate.member_count === 'number' ? candidate.member_count : 0,
    node_count: typeof candidate.node_count === 'number' ? candidate.node_count : 0,
    created_at: typeof candidate.created_at === 'string' ? candidate.created_at : '',
  }
}

/** The list endpoints answer with bare arrays; `{items}` is tolerated too. */
function listOf(data: unknown): unknown[] {
  if (Array.isArray(data)) return data
  const items = (data as { items?: unknown } | null)?.items
  if (!Array.isArray(items)) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  return items
}

function myGroupOf(data: unknown): MyGroup {
  const raw = data as Partial<MyGroup> | null
  if (!raw || typeof raw.id !== 'number' || typeof raw.name !== 'string' || typeof raw.root_id !== 'number') {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return {
    id: raw.id,
    name: raw.name,
    role: raw.role === 'manager' ? 'manager' : 'member',
    root_id: raw.root_id,
    member_perms: permissionsOf(raw.member_perms),
  }
}

function createdGroupOf(data: unknown): CreatedGroup {
  const raw = data as Partial<CreatedGroup> | null
  if (!raw || typeof raw.root_id !== 'number') {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return { group: groupOf(raw), root_id: raw.root_id }
}

function memberOf(data: unknown): GroupMember {
  const raw = data as Partial<GroupMember> | null
  // The contract only pins `user_id`; tolerate a partial member payload.
  if (!raw || typeof raw.user_id !== 'number') {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  return {
    user_id: raw.user_id,
    username: typeof raw.username === 'string' ? raw.username : `#${raw.user_id}`,
    display_name: typeof raw.display_name === 'string' ? raw.display_name : '',
    role: raw.role === 'manager' ? 'manager' : 'member',
  }
}

/** Groups the signed-in user belongs to (drives the sidebar + group pages). */
export function listMyGroups(): Promise<MyGroup[]> {
  return request<unknown>('/api/v1/groups').then((data) => listOf(data).map(myGroupOf))
}

/** Creates a group; the caller automatically becomes its manager. */
export function createGroup(input: CreateGroupInput): Promise<CreatedGroup> {
  return request<unknown>('/api/v1/groups', { method: 'POST', body: input }).then(createdGroupOf)
}

/** Admin console list: system admins see every group, delegates only their own. */
export function listAdminGroups(): Promise<Group[]> {
  return request<unknown>('/api/v1/admin/groups').then((data) => listOf(data).map(groupOf))
}

export function createAdminGroup(input: AdminCreateGroupInput): Promise<CreatedGroup> {
  return request<unknown>('/api/v1/admin/groups', { method: 'POST', body: input }).then(createdGroupOf)
}

export function updateAdminGroup(id: number, input: UpdateGroupInput): Promise<Group> {
  return request<unknown>(`/api/v1/admin/groups/${id}`, { method: 'PATCH', body: input }).then(groupOf)
}

export function deleteAdminGroup(id: number): Promise<void> {
  return request<void>(`/api/v1/admin/groups/${id}`, { method: 'DELETE' })
}

export function listGroupMembers(groupId: number): Promise<GroupMember[]> {
  return request<unknown>(`/api/v1/admin/groups/${groupId}/members`).then((data) => listOf(data).map(memberOf))
}

export function addGroupMember(groupId: number, input: AddGroupMemberInput): Promise<GroupMember> {
  return request<unknown>(`/api/v1/admin/groups/${groupId}/members`, { method: 'POST', body: input }).then(
    (data) => {
      const envelope = data as { member?: unknown } | null
      return memberOf(envelope && envelope.member ? envelope.member : data)
    },
  )
}

export function removeGroupMember(groupId: number, userId: number): Promise<void> {
  return request<void>(`/api/v1/admin/groups/${groupId}/members/${userId}`, { method: 'DELETE' })
}

/* --- admin (M5.2) --- */

export type AdminRole = 'user' | 'admin'
export type AdminUserStatus = 'active' | 'disabled'

export interface AdminUser {
  id: number
  username: string
  display_name: string
  role: string
  status: string
  used_bytes: number
  quota_bytes: number
  reserved_bytes: number
  created_at: string
  last_login_at?: string | null
  locked_until?: string | null
  /** Masked for the console; empty string when the user has no such identifier. */
  email?: string
  phone?: string
  /** Root administrator: the only role allowed to change the registration switch. */
  is_root?: boolean
  /** Delegated ability to create groups (P1); system admins toggle it. */
  can_create_groups?: boolean
  /** Set while the account sits in the deletion grace period (M5.2c). */
  deleted_at?: string | null
  /** RFC3339 moment the account gets purged permanently; null when not deleting. */
  purge_after?: string | null
}

export interface AdminUserPage {
  items: AdminUser[]
  total: number
  limit: number
  offset: number
}

export interface CreateAdminUserInput {
  username: string
  password?: string
  role: AdminRole
  quota_bytes: number
  /** Optional identifiers (M5.2b); at least the username is still required here. */
  email?: string
  phone?: string
  /** Grant group-creation rights at creation time (P1). */
  can_create_groups?: boolean
}

export interface UpdateAdminUserInput {
  status?: AdminUserStatus
  quota_bytes?: number
  role?: AdminRole
  can_create_groups?: boolean
}

export interface CreatedAdminUser {
  user: AdminUser
  initial_password?: string
}

export interface AdminAuditEntry {
  at: string
  actor_id: number
  actor_name: string
  action: string
  target_type: string
  target_id: number
  result: string
  ip: string
  request_id: string
  detail: string
}

export interface AdminAuditPage {
  items: AdminAuditEntry[]
  total: number
  limit: number
  offset: number
}

export interface AdminSettings {
  site_name: string
  registration_enabled: boolean
  /** Root-only switch for self-service account deletion (M5.2c). */
  deletion_enabled: boolean
  default_quota_bytes: number
  audit_retention_days: number
  trash_retention_days: number
}

export interface AdminSettingsPatch {
  site_name?: string
  registration_enabled?: boolean
  deletion_enabled?: boolean
  default_quota_bytes?: number
}

export interface AuditQuery {
  limit?: number
  offset?: number
  actor?: string
  action?: string
  /** Unix seconds. */
  from?: number
  /** Unix seconds. */
  to?: number
}

function adminUserPage(data: unknown): AdminUserPage {
  const page = data as Partial<AdminUserPage> | null
  if (!page || !Array.isArray(page.items)) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  return {
    items: page.items as AdminUser[],
    total: typeof page.total === 'number' ? page.total : page.items.length,
    limit: typeof page.limit === 'number' ? page.limit : 50,
    offset: typeof page.offset === 'number' ? page.offset : 0,
  }
}

export function listAdminUsers({
  limit = 50,
  offset = 0,
  q = '',
}: { limit?: number; offset?: number; q?: string } = {}): Promise<AdminUserPage> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  if (q) query.set('q', q)
  return request<unknown>(`/api/v1/admin/users?${query.toString()}`).then(adminUserPage)
}

export function createAdminUser(input: CreateAdminUserInput): Promise<CreatedAdminUser> {
  return request<CreatedAdminUser>('/api/v1/admin/users', { method: 'POST', body: input }).then((data) => {
    if (!data || !data.user || typeof data.user.id !== 'number') {
      throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
    }
    return data
  })
}

export function updateAdminUser(id: number, input: UpdateAdminUserInput): Promise<AdminUser> {
  return request<{ user: AdminUser }>(`/api/v1/admin/users/${id}`, { method: 'PATCH', body: input }).then(
    (data) => {
      if (!data || !data.user) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
      return data.user
    },
  )
}

/** Lifts the deletion grace period early (M5.2c). */
export function restoreAdminUser(id: number): Promise<AdminUser> {
  return request<{ user: AdminUser }>(`/api/v1/admin/users/${id}/restore`, {
    method: 'POST',
    body: {},
  }).then((data) => {
    if (!data || !data.user) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
    return data.user
  })
}

/** Permanently destroys the account and its files; only valid while deleting. */
export function purgeAdminUser(id: number): Promise<void> {
  return request<void>(`/api/v1/admin/users/${id}/purge`, { method: 'POST', body: {} })
}

export function resetAdminUserPassword(id: number): Promise<string> {
  return request<{ password: string }>(`/api/v1/admin/users/${id}/reset-password`, {
    method: 'POST',
    body: {},
  }).then((data) => {
    if (!data || typeof data.password !== 'string') {
      throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
    }
    return data.password
  })
}

export function listAdminAudit(query: AuditQuery = {}): Promise<AdminAuditPage> {
  const params = new URLSearchParams({
    limit: String(query.limit ?? 50),
    offset: String(query.offset ?? 0),
  })
  if (query.actor) params.set('actor', query.actor)
  if (query.action) params.set('action', query.action)
  if (typeof query.from === 'number' && Number.isFinite(query.from)) params.set('from', String(query.from))
  if (typeof query.to === 'number' && Number.isFinite(query.to)) params.set('to', String(query.to))
  return request<unknown>(`/api/v1/admin/audit?${params.toString()}`).then((data) => {
    const page = data as Partial<AdminAuditPage> | null
    if (!page || !Array.isArray(page.items)) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
    return {
      items: page.items as AdminAuditEntry[],
      total: typeof page.total === 'number' ? page.total : page.items.length,
      limit: typeof page.limit === 'number' ? page.limit : 50,
      offset: typeof page.offset === 'number' ? page.offset : 0,
    }
  })
}

export function listAdminAuditActions(): Promise<string[]> {
  return request<{ actions: string[] }>('/api/v1/admin/audit/actions').then((data) => {
    if (!data || !Array.isArray(data.actions)) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
    return data.actions.filter((action): action is string => typeof action === 'string')
  })
}

export function getAdminSettings(): Promise<AdminSettings> {
  return request<AdminSettings>('/api/v1/admin/settings')
}

export function updateAdminSettings(input: AdminSettingsPatch): Promise<AdminSettings> {
  return request<AdminSettings>('/api/v1/admin/settings', { method: 'PUT', body: input })
}

/* --- storage policies (M5.3) --------------------------------------------- */

export interface CreateStoragePolicyInput {
  name: string
  root_path: string
  enabled?: boolean
  is_default?: boolean
}

export interface UpdateStoragePolicyInput {
  name?: string
  root_path?: string
  enabled?: boolean
  is_default?: boolean
}

function numberField(record: Record<string, unknown>, key: string): number {
  const value = record[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

/** Tolerant parse of one policy: unknown extra fields are simply ignored. */
function storagePolicyFromRecord(value: unknown): StoragePolicyInfo {
  if (typeof value !== 'object' || value === null) {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }
  const record = value as Record<string, unknown>
  if (typeof record.id !== 'number' || typeof record.name !== 'string' || typeof record.root_path !== 'string') {
    throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  }

  return {
    id: record.id,
    name: record.name,
    type: typeof record.type === 'string' ? record.type : '',
    root_path: record.root_path,
    enabled: record.enabled === true,
    is_default: record.is_default === true,
    used_bytes: numberField(record, 'used_bytes'),
    node_count: numberField(record, 'node_count'),
    file_count: numberField(record, 'file_count'),
    created_at: typeof record.created_at === 'string' ? record.created_at : '',
    updated_at: typeof record.updated_at === 'string' ? record.updated_at : '',
  }
}

export async function listAdminStorage(): Promise<StoragePolicyInfo[]> {
  const data = await request<unknown>('/api/v1/admin/storage')
  const items = (data as { items?: unknown } | null)?.items
  if (!Array.isArray(items)) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)
  return items.map(storagePolicyFromRecord)
}

/** 201 `{policy}`; the caller gets the policy itself. */
export async function createAdminStorage(input: CreateStoragePolicyInput): Promise<StoragePolicyInfo> {
  const data = await request<unknown>('/api/v1/admin/storage', { method: 'POST', body: input })
  return storagePolicyFromRecord((data as { policy?: unknown } | null)?.policy)
}

export async function updateAdminStorage(
  id: number,
  input: UpdateStoragePolicyInput,
): Promise<StoragePolicyInfo> {
  const data = await request<unknown>(`/api/v1/admin/storage/${id}`, { method: 'PATCH', body: input })
  return storagePolicyFromRecord((data as { policy?: unknown } | null)?.policy)
}

/** 204; deleting a non-empty/default/last policy answers 409 with a reason. */
export function deleteAdminStorage(id: number): Promise<void> {
  return request<void>(`/api/v1/admin/storage/${id}`, { method: 'DELETE' })
}

/** Every signed-in user: enabled policies only, ordered by id. */
export async function listStoragePolicies(): Promise<StoragePolicyOption[]> {
  const data = await request<unknown>('/api/v1/storage/policies')
  const items = (data as { items?: unknown } | null)?.items
  if (!Array.isArray(items)) throw new ApiError(0, 'invalid_response', INVALID_RESPONSE_MESSAGE)

  const options: StoragePolicyOption[] = []
  for (const item of items) {
    if (typeof item !== 'object' || item === null) continue
    const record = item as Record<string, unknown>
    if (typeof record.id !== 'number' || typeof record.name !== 'string') continue
    options.push({
      id: record.id,
      name: record.name,
      is_default: record.is_default === true,
      enabled: record.enabled !== false,
    })
  }
  return options.sort((a, b) => a.id - b.id)
}
