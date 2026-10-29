#!/usr/bin/env node
/**
 * P1 前端自验：组共享空间（侧栏、组空间、组管理、用户「可创建组」开关）。
 *
 * 不依赖后端、不新装依赖（与 scripts/mock-storage.mjs / mock-transfers-qb.mjs 同一套做法）：
 *  - 内置 mock HTTP 服务，按 docs/20-p1-design.md 的契约返回固定数据；
 *  - 用 ms-playwright 缓存里的 headless Chromium 通过裸 CDP 驱动；
 *  - 静态资源直接读 `web/dist`（先执行 `pnpm run build`）。
 *
 * 用法（Node 需在 PATH 上）：
 *   node scripts/mock-groups.mjs
 * 可用环境变量覆盖：RAPIG_CHROME、RAPIG_SCREENSHOTS（默认 web/.accept）
 */
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = path.resolve(HERE, '..')
const DIST_ROOT = path.join(WEB_ROOT, 'dist')
const SCREENSHOT_DIR = process.env.RAPIG_SCREENSHOTS || path.join(WEB_ROOT, '.accept')

if (!existsSync(path.join(DIST_ROOT, 'index.html'))) {
  console.error(`未找到 ${DIST_ROOT}/index.html，请先在 web/ 下执行 pnpm run build`)
  process.exit(2)
}

/* ------------------------------------------------------------------ mock */
const NOW = '2026-10-28T10:00:00Z'

const state = {
  seq: 100,
  users: [
    { id: 1, username: 'tester', display_name: 'Test User', email: null, phone: null, role: 'admin', status: 'active', quota_bytes: 0, used_bytes: 0, is_root: true, can_create_groups: false, created_at: NOW },
    { id: 3, username: 'zhangsan', display_name: '张三', email: 'a***@example.com', phone: null, role: 'user', status: 'active', quota_bytes: 5_000_000_000, used_bytes: 1_200_000, is_root: false, can_create_groups: false, created_at: NOW },
    { id: 4, username: 'lisi', display_name: '李四', email: null, phone: '138****0000', role: 'user', status: 'active', quota_bytes: 5_000_000_000, used_bytes: 0, is_root: false, can_create_groups: true, created_at: NOW },
  ],
  groups: [
    { id: 1, name: '设计部', role: 'member', root_id: 1000, member_perms: ['read', 'upload', 'create'] },
    { id: 2, name: '财务组', role: 'manager', root_id: 2000, member_perms: ['read'] },
  ],
  adminGroups: [
    { id: 1, name: '设计部', member_count: 2, node_count: 2, created_by: 1, created_at: '2026-10-01T08:00:00Z' },
    { id: 2, name: '财务组', member_count: 1, node_count: 1, created_by: 1, created_at: '2026-10-02T08:00:00Z' },
  ],
  members: {
    1: [
      { user_id: 3, username: 'zhangsan', display_name: '张三', role: 'manager' },
      { user_id: 1, username: 'tester', display_name: 'Test User', role: 'member' },
    ],
    2: [{ user_id: 1, username: 'tester', display_name: 'Test User', role: 'manager' }],
  },
  nodes: {
    0: [],
    1000: [
      node(1001, 1000, 'file', '说明.pdf', 2048),
      node(1002, 1000, 'dir', '资料', 0),
    ],
    2000: [node(2001, 2000, 'file', '预算.xlsx', 4096)],
  },
}

function node(id, parentId, kind, name, size) {
  return {
    id,
    parent_id: parentId,
    kind,
    name,
    size,
    revision: '1',
    created_at: '2026-10-01T08:00:00Z',
    updated_at: '2026-10-01T08:00:00Z',
  }
}

const apiLog = []

function sendJson(res, status, payload) {
  const body = JSON.stringify(payload)
  res.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'content-length': Buffer.byteLength(body) })
  res.end(body)
}

function sendError(res, status, code, message, extra = {}) {
  sendJson(res, status, { error: { code, message, request_id: 'mock', ...extra } })
}

function readBody(req) {
  return new Promise((resolve) => {
    const chunks = []
    req.on('data', (chunk) => chunks.push(chunk))
    req.on('end', () => {
      const raw = Buffer.concat(chunks).toString('utf8')
      try {
        resolve(raw ? JSON.parse(raw) : {})
      } catch {
        resolve({})
      }
    })
  })
}

function requestPath(rawUrl) {
  try {
    return new URL(rawUrl, 'http://127.0.0.1')
  } catch {
    return new URL('http://127.0.0.1/')
  }
}

function currentUser() {
  return state.users[0]
}

function meJson() {
  const user = currentUser()
  return {
    id: user.id,
    username: user.username,
    role: user.role,
    used_bytes: user.used_bytes,
    quota_bytes: user.quota_bytes,
    email: user.email,
    phone: user.phone,
    can_create_groups: user.can_create_groups,
    is_root: user.is_root,
  }
}

function adminUserJson(user) {
  return { ...user }
}

function userPage(query) {
  const q = (query.get('q') ?? '').trim().toLowerCase()
  const items = state.users.filter(
    (user) => !q || user.username.toLowerCase().includes(q) || (user.display_name ?? '').toLowerCase().includes(q),
  )
  return { items: items.map(adminUserJson), total: items.length, limit: 50, offset: 0 }
}

async function handleApi(req, res, rawUrl) {
  const url = requestPath(rawUrl)
  const pathname = url.pathname
  const method = req.method ?? 'GET'
  const body = method === 'GET' || method === 'HEAD' ? {} : await readBody(req)
  apiLog.push({ method, pathname, search: url.search, body })

  if (pathname === '/api/v1/auth/me') return sendJson(res, 200, { user: meJson() })
  if (pathname === '/api/v1/auth/registration') return sendJson(res, 200, { enabled: false })
  if (pathname === '/api/v1/auth/logout') {
    res.writeHead(204)
    return res.end()
  }

  /* my groups */
  if (pathname === '/api/v1/groups' && method === 'GET') return sendJson(res, 200, state.groups)
  if (pathname === '/api/v1/groups' && method === 'POST') {
    const id = state.seq++
    const rootId = 9000 + id
    const group = {
      id,
      name: String(body.name ?? '新组'),
      member_perms: Array.isArray(body.member_perms) && body.member_perms.length ? body.member_perms : ['read'],
    }
    state.groups.push({ ...group, role: 'manager', root_id: rootId })
    state.adminGroups.unshift({ id, name: group.name, member_count: 1, node_count: 0, created_by: 1, created_at: NOW })
    state.members[id] = [{ user_id: 1, username: 'tester', display_name: 'Test User', role: 'manager' }]
    state.nodes[rootId] = []
    return sendJson(res, 201, { group, root_id: rootId })
  }

  /* nodes */
  if (pathname === '/api/v1/nodes' && method === 'GET') {
    const parent = Number(url.searchParams.get('parent') ?? '0')
    const items = state.nodes[parent] ?? []
    return sendJson(res, 200, { items, total: items.length, limit: 200, offset: 0 })
  }
  if (pathname === '/api/v1/nodes' && method === 'POST') {
    const parent = Number(body.parent ?? 0)
    const id = state.seq++
    const created = node(id, parent, body.kind === 'dir' ? 'dir' : 'file', String(body.name ?? '未命名'), 0)
    if (!state.nodes[parent]) state.nodes[parent] = []
    state.nodes[parent].push(created)
    const group = state.groups.find((entry) => entry.root_id === parent)
    if (group) {
      const adminGroup = state.adminGroups.find((entry) => entry.id === group.id)
      if (adminGroup) adminGroup.node_count += 1
    }
    return sendJson(res, 201, created)
  }

  const nodeOne = /^\/api\/v1\/nodes\/(\d+)$/.exec(pathname)
  if (nodeOne && method === 'DELETE') {
    const id = Number(nodeOne[1])
    for (const key of Object.keys(state.nodes)) {
      const list = state.nodes[key]
      const index = list.findIndex((item) => item.id === id)
      if (index >= 0) list.splice(index, 1)
    }
    res.writeHead(204)
    return res.end()
  }
  if (nodeOne && method === 'PATCH') {
    const id = Number(nodeOne[1])
    for (const key of Object.keys(state.nodes)) {
      const found = state.nodes[key].find((item) => item.id === id)
      if (found) {
        if (typeof body.name === 'string') found.name = body.name
        if (typeof body.parent === 'number') found.parent_id = body.parent
        found.revision = String(Number(found.revision) + 1)
        return sendJson(res, 200, found)
      }
    }
    return sendError(res, 404, 'not_found', 'node not found')
  }

  if (pathname === '/api/v1/trash') return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  if (pathname === '/api/v1/shares') return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  if (pathname === '/api/v1/transfers') return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  if (pathname === '/api/v1/transfers/qb/status') return sendJson(res, 200, { enabled: false })

  /* admin groups */
  if (pathname === '/api/v1/admin/groups' && method === 'GET') {
    return sendJson(res, 200, state.adminGroups.map((entry) => ({ ...entry })))
  }
  if (pathname === '/api/v1/admin/groups' && method === 'POST') {
    const id = state.seq++
    const created = {
      id,
      name: String(body.name ?? '新组'),
      member_perms: Array.isArray(body.member_perms) && body.member_perms.length ? body.member_perms : ['read'],
    }
    const managerId = Number(body.manager_user_id ?? 1)
    const manager = state.users.find((user) => user.id === managerId) ?? currentUser()
    state.groups.push({ ...created, role: 'manager', root_id: 9000 + id })
    state.adminGroups.unshift({ ...created, member_count: 1, node_count: 0, created_by: manager.id, created_at: NOW })
    state.members[id] = [{ user_id: manager.id, username: manager.username, display_name: manager.display_name, role: 'manager' }]
    state.nodes[9000 + id] = []
    return sendJson(res, 201, { group: created, root_id: 9000 + id })
  }

  const adminGroupOne = /^\/api\/v1\/admin\/groups\/(\d+)$/.exec(pathname)
  if (adminGroupOne && method === 'PATCH') {
    const id = Number(adminGroupOne[1])
    const target = state.adminGroups.find((entry) => entry.id === id)
    if (!target) return sendError(res, 404, 'not_found', 'group not found')
    if (typeof body.name === 'string') target.name = body.name
    return sendJson(res, 200, { group: { ...target } })
  }
  if (adminGroupOne && method === 'DELETE') {
    const id = Number(adminGroupOne[1])
    const target = state.adminGroups.find((entry) => entry.id === id)
    if (!target) return sendError(res, 404, 'not_found', 'group not found')
    if (target.node_count > 0) {
      return sendError(res, 409, 'invalid_state', 'group not empty', { reason: 'group_not_empty' })
    }
    state.adminGroups = state.adminGroups.filter((entry) => entry.id !== id)
    state.groups = state.groups.filter((entry) => entry.id !== id)
    res.writeHead(204)
    return res.end()
  }

  const members = /^\/api\/v1\/admin\/groups\/(\d+)\/members$/.exec(pathname)
  if (members && method === 'GET') {
    return sendJson(res, 200, state.members[Number(members[1])] ?? [])
  }
  if (members && method === 'POST') {
    const groupId = Number(members[1])
    const userId = Number(body.user_id)
    const user = state.users.find((entry) => entry.id === userId)
    if (!user) return sendError(res, 404, 'not_found', 'user not found')
    const role = body.role === 'manager' ? 'manager' : 'member'
    const list = state.members[groupId] ?? (state.members[groupId] = [])
    const existing = list.find((entry) => entry.user_id === userId)
    if (existing) {
      const managers = list.filter((entry) => entry.role === 'manager')
      if (existing.role === 'manager' && role === 'member' && managers.length <= 1) {
        return sendError(res, 409, 'invalid_state', 'last manager', { reason: 'last_group_manager' })
      }
      existing.role = role
    } else {
      list.push({ user_id: user.id, username: user.username, display_name: user.display_name, role })
    }
    return sendJson(res, 201, {
      user_id: userId,
      username: user.username,
      display_name: user.display_name,
      role,
    })
  }

  const memberOne = /^\/api\/v1\/admin\/groups\/(\d+)\/members\/(\d+)$/.exec(pathname)
  if (memberOne && method === 'DELETE') {
    const groupId = Number(memberOne[1])
    const userId = Number(memberOne[2])
    const list = state.members[groupId] ?? []
    const target = list.find((entry) => entry.user_id === userId)
    if (target && target.role === 'manager' && list.filter((entry) => entry.role === 'manager').length <= 1) {
      return sendError(res, 409, 'invalid_state', 'last manager', { reason: 'last_group_manager' })
    }
    state.members[groupId] = list.filter((entry) => entry.user_id !== userId)
    res.writeHead(204)
    return res.end()
  }

  /* admin users */
  if (pathname === '/api/v1/admin/users' && method === 'GET') return sendJson(res, 200, userPage(url.searchParams))
  const adminUserOne = /^\/api\/v1\/admin\/users\/(\d+)$/.exec(pathname)
  if (adminUserOne && method === 'PATCH') {
    const id = Number(adminUserOne[1])
    const user = state.users.find((entry) => entry.id === id)
    if (!user) return sendError(res, 404, 'not_found', 'user not found')
    if (typeof body.can_create_groups === 'boolean') user.can_create_groups = body.can_create_groups
    if (typeof body.role === 'string') user.role = body.role
    if (typeof body.quota_bytes === 'number') user.quota_bytes = body.quota_bytes
    return sendJson(res, 200, adminUserJson(user))
  }

  if (pathname === '/api/v1/admin/audit/actions') return sendJson(res, 200, { actions: [] })
  if (pathname === '/api/v1/admin/audit') return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  if (pathname === '/api/v1/admin/storage') {
    return sendJson(res, 200, { used_bytes: 0, quota_bytes: 0, trash_bytes: 0, blob_bytes: 0, node_count: 0, blob_count: 0 })
  }
  if (pathname === '/api/v1/admin/settings') {
    return sendJson(res, 200, {
      site_name: 'Rapig',
      registration_enabled: false,
      default_quota_bytes: 0,
      audit_retention_days: 180,
      trash_retention_days: 30,
      deletion_enabled: false,
    })
  }

  return sendError(res, 404, 'not_found', `mock 未实现：${method} ${pathname}`)
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
}

function serveStatic(res, pathname) {
  const safePath = path.normalize(pathname).replace(/^([/\\])+/, '')
  const filePath = path.join(DIST_ROOT, safePath)
  if (!filePath.startsWith(DIST_ROOT)) {
    res.writeHead(403)
    return res.end('forbidden')
  }
  let target = filePath
  if (!existsSync(target) || statSync(target).isDirectory()) target = path.join(DIST_ROOT, 'index.html')
  const bytes = readFileSync(target)
  res.writeHead(200, {
    'content-type': MIME[path.extname(target).toLowerCase()] ?? 'application/octet-stream',
    'cache-control': 'no-store',
    'content-length': bytes.length,
  })
  res.end(bytes)
}

function startMockServer() {
  const server = http.createServer((req, res) => {
    const url = req.url ?? '/'
    if (url.startsWith('/api/')) {
      handleApi(req, res, url).catch((error) => sendError(res, 500, 'internal', `mock 内部错误：${error.message}`))
      return
    }
    if (url === '/health') {
      res.writeHead(200, { 'content-type': 'text/plain' })
      return res.end('ok')
    }
    serveStatic(res, requestPath(url).pathname)
  })
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)))
}

/* ------------------------------------------------------------------- CDP */
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

async function freePort() {
  return await new Promise((resolve, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolve(port))
    })
  })
}

async function resolveChrome() {
  if (process.env.RAPIG_CHROME) {
    if (!existsSync(process.env.RAPIG_CHROME)) throw new Error(`RAPIG_CHROME 不存在：${process.env.RAPIG_CHROME}`)
    return process.env.RAPIG_CHROME
  }
  const { readdirSync } = await import('node:fs')
  const root = path.join(os.homedir(), 'AppData', 'Local', 'ms-playwright')
  if (!existsSync(root)) throw new Error('未找到 ms-playwright 缓存，请设置 RAPIG_CHROME')
  const rels = [
    ['chromium_headless_shell', 'chrome-headless-shell-win64', 'chrome-headless-shell.exe'],
    ['chromium', 'chrome-win64', 'chrome.exe'],
    ['chromium', 'chrome-win', 'chrome.exe'],
  ]
  for (const dir of readdirSync(root).sort().reverse()) {
    for (const rel of rels) {
      if (!dir.startsWith(rel[0])) continue
      const full = path.join(root, dir, ...rel.slice(1))
      if (existsSync(full)) return full
    }
  }
  throw new Error('未找到 Chromium 可执行文件，请设置 RAPIG_CHROME')
}

function connectWebSocket(url) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url)
    socket.onopen = () => resolve(socket)
    socket.onerror = () => reject(new Error(`无法连接 CDP：${url}`))
  })
}

function createCdp(socket) {
  let nextId = 1
  const pending = new Map()
  const pageErrors = []
  socket.onmessage = (event) => {
    const raw = typeof event.data === 'string' ? event.data : Buffer.from(event.data).toString('utf8')
    const message = JSON.parse(raw)
    if (message.id !== undefined && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(`${message.error.message} (${message.error.code})`))
      else resolve(message.result)
      return
    }
    if (message.method === 'Runtime.exceptionThrown') {
      const details = message.params?.exceptionDetails
      pageErrors.push(details?.exception?.description ?? details?.text ?? 'unknown page error')
    }
  }
  function send(method, params = {}, sessionId) {
    return new Promise((resolve, reject) => {
      const id = nextId++
      pending.set(id, { resolve, reject })
      socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
    })
  }
  return { send, pageErrors }
}

async function launchBrowser(port, userDataDir, chromePath) {
  const isHeadlessShell = /headless[_-]?shell/i.test(path.basename(chromePath))
  const args = [
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${userDataDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--disable-dev-shm-usage',
    '--disable-background-networking',
    '--disable-extensions',
    '--mute-audio',
    '--hide-scrollbars',
    '--window-size=1280,900',
    'about:blank',
  ]
  if (!isHeadlessShell) args.unshift('--headless=new')
  const child = spawn(chromePath, args, { stdio: 'ignore' })
  const started = Date.now()
  while (Date.now() - started < 15_000) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) {
        const version = await response.json()
        return { child, wsUrl: version.webSocketDebuggerUrl }
      }
    } catch {
      // still booting
    }
    await sleep(200)
  }
  child.kill()
  throw new Error('Chromium 启动超时（CDP 端口未就绪）')
}

/* ------------------------------------------------------------------ test */
const checks = []
let failed = 0

function check(name, ok, detail = '') {
  checks.push({ name, ok })
  if (!ok) failed += 1
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`)
}

async function main() {
  mkdirSync(SCREENSHOT_DIR, { recursive: true })
  const server = await startMockServer()
  const { port } = server.address()
  const baseUrl = `http://127.0.0.1:${port}`
  const chromePath = await resolveChrome()
  const cdpPort = await freePort()
  const userDataDir = mkdtempSync(path.join(os.tmpdir(), 'rapig-mock-groups-'))
  const browser = await launchBrowser(cdpPort, userDataDir, chromePath)
  const socket = await connectWebSocket(browser.wsUrl)
  const cdp = createCdp(socket)

  try {
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
    await cdp.send('Page.enable', {}, sessionId)
    await cdp.send('Runtime.enable', {}, sessionId)
    await cdp.send(
      'Network.setCookie',
      { name: 'rapig_csrf', value: 'mock-csrf-token', domain: '127.0.0.1', path: '/', sameSite: 'Lax' },
      sessionId,
    )

    const raw = async (expression) => {
      const result = await cdp.send(
        'Runtime.evaluate',
        { expression, awaitPromise: true, returnByValue: true, userGesture: true },
        sessionId,
      )
      if (result.exceptionDetails) {
        throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text ?? 'evaluate 失败')
      }
      return result
    }
    const evaluate = async (expression) => (await raw(expression)).result.value

    const waitFor = async (expression, label, timeout = 8000) => {
      const started = Date.now()
      while (Date.now() - started < timeout) {
        try {
          if (await evaluate(`Boolean(${expression})`)) return
        } catch {
          // context may vanish during navigation
        }
        await sleep(60)
      }
      throw new Error(`等待超时：${label}`)
    }

    const navigate = async (route) => {
      await cdp.send('Page.navigate', { url: `${baseUrl}${route}` }, sessionId)
      await sleep(120)
      await waitFor(`document.readyState === 'complete'`, `页面加载 ${route}`, 12_000)
      await waitFor(`document.querySelector('.page') !== null`, `应用挂载 ${route}`, 12_000)
    }

    const text = async (selector) =>
      await evaluate(`document.querySelector(${JSON.stringify(selector)})?.textContent ?? ''`)

    const clickText = async (selector, label) => {
      const ok = await evaluate(
        `(() => {
          const item = [...document.querySelectorAll(${JSON.stringify(selector)})].find((el) => el.textContent.trim() === ${JSON.stringify(label)})
          if (!item) return false
          item.click()
          return true
        })()`,
      )
      if (!ok) throw new Error(`找不到文本为「${label}」的元素：${selector}`)
    }

    const clickRowAction = async (rowText, label) => {
      const ok = await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.row')].find((el) => el.textContent.includes(${JSON.stringify(rowText)}))
          if (!row) return false
          const button = row.querySelector('.row-menu-button')
          if (!button) return false
          button.click()
          return true
        })()`,
      )
      if (!ok) throw new Error(`找不到行「${rowText}」的三点菜单`)
      await waitFor(`document.querySelector('.row-menu') !== null`, '行菜单打开')
      await clickText('.row-menu-item', label)
    }

    const setInput = async (selector, value) => {
      const ok = await evaluate(
        `(() => {
          const el = document.querySelector(${JSON.stringify(selector)})
          if (!el) return false
          const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
          Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, ${JSON.stringify(value)})
          el.dispatchEvent(new Event('input', { bubbles: true }))
          return true
        })()`,
      )
      if (!ok) throw new Error(`找不到输入框：${selector}`)
    }

    const shot = async (name) => {
      await sleep(260)
      const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
      rmSync(path.join(SCREENSHOT_DIR, name), { force: true })
      const { writeFileSync } = await import('node:fs')
      writeFileSync(path.join(SCREENSHOT_DIR, name), Buffer.from(result.data, 'base64'))
    }

    /* -- 1. 侧栏组空间 -- */
    await navigate('/files')
    await waitFor(`document.querySelectorAll('.nav-item-group').length >= 2`, '侧栏组空间')
    const navText = await evaluate(`
      [...document.querySelectorAll('.nav-item-group')].map((el) => el.querySelector('.nav-group-name')?.textContent?.trim() ?? '')
    `)
    check('侧栏出现「组空间」并列出我的两个组', Array.isArray(navText) && navText.length === 2 && navText[0].includes('设计部'), JSON.stringify(navText))
    check('侧栏显示「组空间」分组标题', (await text('.nav-group-label')).includes('组空间'))

    /* -- 2. 进入成员组空间 -- */
    await evaluate(`document.querySelectorAll('.nav-item-group')[0]?.click()`)
    await waitFor(`location.pathname === '/groups/1'`, '进入组空间')
    await waitFor(`document.querySelectorAll('.row').length === 2`, '组文件列表')
    const groupRows = await evaluate(`[...document.querySelectorAll('.row .name')].map((el) => el.textContent.trim())`)
    check('组空间列表显示组内文件', JSON.stringify(groupRows) === JSON.stringify(['说明.pdf', '资料']), JSON.stringify(groupRows))
    check('面包屑从组名开始', (await text('.breadcrumb')).includes('设计部'))
    await shot('web-groups-1280.png')

    /* -- 3. 成员权限：无删除项 -- */
    await evaluate(`document.querySelector('.row .row-menu-button')?.click()`)
    await waitFor(`document.querySelector('.row-menu') !== null`, '行菜单')
    const memberMenu = await evaluate(`[...document.querySelectorAll('.row-menu-item')].map((el) => el.textContent.trim())`)
    check('成员无「删除」权限时行菜单没有删除', memberMenu.includes('预览') && !memberMenu.includes('删除'), JSON.stringify(memberMenu))
    await evaluate(`document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))`)

    /* -- 4. 新建文件夹 -- */
    const beforeCreate = await evaluate(`document.querySelectorAll('.row').length`)
    await clickText('.page-actions .btn', '新建文件夹')
    await waitFor(`document.querySelector('#folder-dialog-title') !== null`, '新建文件夹对话框')
    await setInput('.dialog input', '组内目录')
    await clickText('.dialog-actions .btn-primary', '创建')
    await waitFor(`document.querySelectorAll('.row').length === ${beforeCreate + 1}`, '组内目录创建完成')
    const createCall = apiLog.filter((entry) => entry.method === 'POST' && entry.pathname === '/api/v1/nodes').pop()
    check('在组空间新建文件夹使用了组根 parent', createCall?.body?.parent === 1000, JSON.stringify(createCall?.body))

    /* -- 5. 管理员组：永久删除确认 -- */
    await evaluate(`
      (() => {
        const item = [...document.querySelectorAll('.nav-item-group')].find((el) => el.textContent.includes('财务组'))
        item?.click()
        return Boolean(item)
      })()
    `)
    await waitFor(`location.pathname === '/groups/2'`, '进入财务组')
    await waitFor(`document.querySelectorAll('.row').length === 1`, '财务组文件列表')
    await clickRowAction('预算.xlsx', '删除')
    await waitFor(`document.querySelector('.dialog') !== null`, '永久删除确认')
    const dialogText = await text('.dialog')
    check('永久删除确认标题与不可恢复说明', dialogText.includes('永久删除') && dialogText.includes('不可恢复') && dialogText.includes('释放存储空间'), dialogText.slice(0, 80))
    await shot('web-groups-delete.png')
    const beforeDelete = apiLog.length
    await clickText('.dialog-actions .btn-danger', '永久删除')
    await waitFor(`document.querySelectorAll('.row').length === 0`, '删除后列表为空')
    const deleteCall = apiLog.slice(beforeDelete).find((entry) => entry.method === 'DELETE')
    check('组节点删除走普通 DELETE（无 purge 参数）', Boolean(deleteCall) && deleteCall.search === '', JSON.stringify(deleteCall))

    /* -- 6. 组管理页 -- */
    await navigate('/admin/groups')
    await waitFor(`document.querySelectorAll('.group-table tbody tr').length === 2`, '组管理列表')
    const adminRows = await evaluate(`[...document.querySelectorAll('.group-table tbody tr')].map((el) => el.textContent.trim())`)
    check('组管理页列出全部组（管理员）', adminRows.length === 2 && adminRows[0].includes('设计部'), JSON.stringify(adminRows.slice(0, 1)))
    await shot('web-admin-groups-1280.png')

    await clickText('.page-actions .btn', '新建组')
    await waitFor(`document.querySelector('#group-create-title') !== null`, '新建组对话框')
    await setInput('#group-name', '测试组')
    await clickText('.dialog-actions .btn-primary', '创建组')
    await waitFor(`document.querySelectorAll('.group-table tbody tr').length === 3`, '新组出现在列表')
    const groupCreate = apiLog.filter((entry) => entry.method === 'POST' && entry.pathname === '/api/v1/admin/groups').pop()
    check('新建组提交名称与组员权限', groupCreate?.body?.name === '测试组' && Array.isArray(groupCreate?.body?.member_perms), JSON.stringify(groupCreate?.body))

    /* -- 7. 成员抽屉 -- */
    await evaluate(`
      (() => {
        const row = [...document.querySelectorAll('.group-table tbody tr')].find((el) => el.textContent.includes('设计部'))
        const button = row && [...row.querySelectorAll('.text-button')].find((el) => el.textContent.trim() === '成员')
        button?.click()
        return Boolean(button)
      })()
    `)
    await waitFor(`document.querySelectorAll('.member-row').length === 2`, '成员列表')
    await shot('web-admin-members-1280.png')
    await evaluate(`
      (() => {
        const row = [...document.querySelectorAll('.member-row')].find((el) => el.textContent.includes('tester'))
        const select = row?.querySelector('select')
        if (!select) return false
        select.value = 'manager'
        select.dispatchEvent(new Event('change', { bubbles: true }))
        return true
      })()
    `)
    await waitFor(`document.body.innerText.includes('已将 tester 设为管理员')`, '角色切换提示')
    check('成员角色切换提交 /members', apiLog.some((entry) => entry.method === 'POST' && entry.pathname === '/api/v1/admin/groups/1/members'), 'ok')
    await setInput('#member-search', 'lisi')
    await waitFor(`document.querySelectorAll('.search-result').length >= 1`, '用户搜索结果')
    await clickText('.search-result .btn', '加入')
    await waitFor(`document.body.innerText.includes('已加入成员')`, '加入成员提示')
    check('搜索用户名添加成员', apiLog.filter((entry) => entry.method === 'POST' && entry.pathname === '/api/v1/admin/groups/1/members').length >= 2, 'ok')

    /* -- 8. 非空组删除失败提示 -- */
    await navigate('/admin/groups')
    await waitFor(`document.querySelectorAll('.group-table tbody tr').length === 3`, '组管理列表')
    await evaluate(`
      (() => {
        const row = [...document.querySelectorAll('.group-table tbody tr')].find((el) => el.textContent.includes('设计部'))
        const button = row && row.querySelector('.text-button.is-danger')
        button?.click()
        return Boolean(button)
      })()
    `)
    await waitFor(`document.querySelector('.dialog') !== null`, '删除组确认')
    await clickText('.dialog-actions .btn-danger', '删除组')
    await waitFor(`document.querySelector('.dialog-error') !== null`, '删除失败提示')
    const deleteError = await text('.dialog-error')
    check('非空组删除提示与后端 reason 一致', deleteError.includes('非空'), deleteError)

    /* -- 9. 用户「可创建组」开关 -- */
    await navigate('/admin/users')
    await waitFor(`document.querySelectorAll('.table tbody tr').length >= 1`, '用户列表')
    await clickText('.table tbody tr:first-child .text-button', '编辑')
    await waitFor(`document.querySelector('.edit-switch') !== null`, '编辑用户对话框')
    await evaluate(`document.querySelector('.edit-switch-input').click()`)
    await shot('web-admin-users-cancreate.png')
    await clickText('.dialog-actions .btn-primary', '保存')
    await sleep(700)
    const patch = apiLog.filter((entry) => entry.method === 'PATCH' && entry.pathname === '/api/v1/admin/users/1').pop()
    if (!patch) {
      const dialogText = await evaluate(`document.querySelector('.dialog')?.textContent ?? ''`)
      console.log('DEBUG no PATCH; dialog =', dialogText.slice(0, 200), '| pageErrors =', JSON.stringify(cdp.pageErrors.slice(0, 2)))
    }
    check('编辑用户保存 can_create_groups', patch?.body?.can_create_groups === true, JSON.stringify(patch?.body))

    /* -- 10. 390px -- */
    await cdp.send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true }, sessionId)
    await navigate('/groups/1')
    await waitFor(`document.querySelector('.page') !== null`, '窄屏组空间')
    const overflow = await evaluate(`document.documentElement.scrollWidth - document.documentElement.clientWidth`)
    check('390px 组空间无横向溢出', overflow === 0, String(overflow))
    await shot('web-groups-390.png')
    await cdp.send('Emulation.clearDeviceMetricsOverride', {}, sessionId)

    check('无页面异常', cdp.pageErrors.length === 0, JSON.stringify(cdp.pageErrors.slice(0, 3)))
  } finally {
    socket.close?.()
    browser.child.kill()
    await sleep(400)
    server.close()
    try {
      rmSync(userDataDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 })
    } catch {
      // Windows may still hold a lock on the profile; the temp dir is harmless.
    }
  }

  console.log(`\n${checks.length} checks, ${failed} failed`)
  if (failed === 0) console.log('ALL PASS')
  process.exitCode = failed === 0 ? 0 : 1
}

await main()
