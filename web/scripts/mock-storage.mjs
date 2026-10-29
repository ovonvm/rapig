#!/usr/bin/env node
/**
 * M5.3 前端自验：多存储策略（管理页 / 根目录存储选择 / 跨策略报错）。
 *
 * 不依赖后端、不新装依赖：
 *  - 内置 mock HTTP 服务，按 `web/README` 里的后端契约返回固定数据；
 *  - 用系统已安装的 Chromium（Playwright 缓存里的 headless shell）通过裸 CDP 驱动；
 *  - 静态资源直接读 `web/dist`（先跑 `pnpm run build`）。
 *
 * 用法（Node 需在 PATH 上）：
 *   node scripts/mock-storage.mjs
 * 可用环境变量覆盖：
 *   RAPIG_CHROME    Chromium / chrome-headless-shell 绝对路径
 *   RAPIG_SCREENSHOTS 截图输出目录（默认 web/.accept）
 */
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
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
const policyDefaults = {
  type: 'local',
  enabled: true,
  is_default: false,
  used_bytes: 0,
  node_count: 0,
  file_count: 0,
  created_at: '2026-10-01T08:00:00Z',
  updated_at: '2026-10-20T09:30:00Z',
}

const state = {
  role: 'admin',
  policies: [
    {
      ...policyDefaults,
      id: 1,
      name: '主存储',
      root_path: 'D:\\rapig\\storage',
      enabled: true,
      is_default: true,
      used_bytes: 1288490188,
      node_count: 3,
      file_count: 2,
    },
    {
      ...policyDefaults,
      id: 2,
      name: '备份盘',
      root_path: '/mnt/disk2/rapig',
      enabled: true,
      is_default: false,
    },
    {
      ...policyDefaults,
      id: 3,
      name: '归档',
      root_path: 'E:\\rapig\\archive',
      enabled: false,
      is_default: false,
    },
  ],
  nextPolicyId: 4,
  failStorageList: false,
  nodes: {
    0: [
      { id: 2, parent_id: 0, kind: 'dir', name: '文档', size: 0, revision: 1, created_at: policyDefaults.created_at, updated_at: policyDefaults.updated_at },
      { id: 10, parent_id: 0, kind: 'file', name: 'a.txt', size: 1024, revision: 1, created_at: policyDefaults.created_at, updated_at: policyDefaults.updated_at },
    ],
    2: [
      { id: 20, parent_id: 2, kind: 'file', name: 'b.txt', size: 2048, revision: 1, created_at: policyDefaults.created_at, updated_at: policyDefaults.updated_at },
    ],
  },
  nextNodeId: 30,
}

/** 每个 API 请求都记下来，断言直接查这张表。 */
const apiLog = []

function record(method, pathname, url, body) {
  apiLog.push({ method, pathname, url, body })
}

function jsonResponse(res, status, body) {
  const payload = JSON.stringify(body)
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'cache-control': 'no-store',
    'content-length': Buffer.byteLength(payload),
  })
  res.end(payload)
}

function apiError(res, status, code, message, reason) {
  jsonResponse(res, status, { error: { code, message, request_id: 'mock', reason: reason ?? null } })
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    req.on('data', (chunk) => chunks.push(chunk))
    req.on('end', () => {
      const text = Buffer.concat(chunks).toString('utf8')
      if (!text) return resolve({})
      try {
        resolve(JSON.parse(text))
      } catch {
        reject(new Error('invalid json body'))
      }
    })
    req.on('error', reject)
  })
}

function nodePage(parent, limit = 50, offset = 0) {
  const all = state.nodes[parent] ?? []
  return {
    items: all.slice(offset, offset + limit),
    total: all.length,
    limit,
    offset,
  }
}

function policiesForUser() {
  return state.policies
    .filter((policy) => policy.enabled)
    .sort((a, b) => a.id - b.id)
    .map((policy) => ({ id: policy.id, name: policy.name, is_default: policy.is_default, enabled: policy.enabled }))
}

function requestPath(url) {
  return url.split('?')[0]
}

async function handleApi(req, res, url) {
  const pathname = requestPath(url)
  const params = new URL(url, 'http://127.0.0.1').searchParams
  const body = req.method === 'GET' || req.method === 'DELETE' ? {} : await readBody(req)
  record(req.method, pathname, url, body)

  if (pathname === '/api/v1/auth/me') {
    return jsonResponse(res, 200, {
      user: {
        id: 1,
        username: state.role === 'admin' ? 'admin' : 'alice',
        role: state.role,
        is_root: state.role === 'admin',
        display_name: state.role === 'admin' ? '管理员' : '成员',
        quota_bytes: 0,
        used_bytes: 0,
        created_at: policyDefaults.created_at,
      },
    })
  }

  if (pathname.startsWith('/api/v1/admin/') && state.role !== 'admin') {
    return apiError(res, 403, 'forbidden', '需要管理员权限')
  }

  if (pathname === '/api/v1/admin/storage' && req.method === 'GET') {
    if (state.failStorageList) return apiError(res, 500, 'internal', '存储服务暂时不可用')
    return jsonResponse(res, 200, { items: state.policies })
  }

  if (pathname === '/api/v1/admin/storage' && req.method === 'POST') {
    const name = typeof body.name === 'string' ? body.name.trim() : ''
    const rootPath = typeof body.root_path === 'string' ? body.root_path.trim() : ''
    if (!name) return apiError(res, 400, 'invalid_name', '名称不能为空')
    if (!rootPath) return apiError(res, 400, 'invalid_path', '根路径不能为空')
    if (state.policies.some((policy) => policy.name === name)) {
      return apiError(res, 409, 'conflict', '存储名称已存在', 'name_conflict')
    }
    const policy = {
      ...policyDefaults,
      id: state.nextPolicyId++,
      name,
      root_path: rootPath,
      enabled: body.enabled !== false,
      is_default: body.is_default === true,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    if (policy.is_default) {
      state.policies = state.policies.map((item) => ({ ...item, is_default: false }))
    }
    state.policies.push(policy)
    return jsonResponse(res, 201, { policy })
  }

  const storageMatch = pathname.match(/^\/api\/v1\/admin\/storage\/(\d+)$/)
  if (storageMatch) {
    const id = Number(storageMatch[1])
    const policy = state.policies.find((item) => item.id === id)

    if (req.method === 'PATCH') {
      if (!policy) return apiError(res, 404, 'not_found', '存储不存在')
      if (typeof body.root_path === 'string' && body.root_path !== policy.root_path) {
        if (policy.node_count + policy.file_count > 0) {
          return apiError(res, 409, 'conflict', '该存储已有数据，不能修改根路径', 'storage_in_use')
        }
        policy.root_path = body.root_path
      }
      if (typeof body.name === 'string' && body.name) policy.name = body.name
      if (typeof body.enabled === 'boolean') policy.enabled = body.enabled
      if (typeof body.is_default === 'boolean' && body.is_default !== policy.is_default) {
        policy.is_default = body.is_default
        if (body.is_default) {
          state.policies = state.policies.map((item) => (item.id === id ? item : { ...item, is_default: false }))
        }
      }
      policy.updated_at = new Date().toISOString()
      return jsonResponse(res, 200, { policy })
    }

    if (req.method === 'DELETE') {
      if (!policy) return apiError(res, 404, 'not_found', '存储不存在')
      if (state.policies.length === 1) {
        return apiError(res, 409, 'conflict', '至少要保留一个存储，不能删除最后一个', 'last_policy')
      }
      if (policy.is_default) {
        return apiError(res, 409, 'conflict', '默认存储不能删除，请先把其它存储设为默认', 'default_policy')
      }
      if (policy.node_count + policy.file_count > 0) {
        return apiError(res, 409, 'conflict', '该存储下还有节点或文件，不能删除', 'storage_in_use')
      }
      state.policies = state.policies.filter((item) => item.id !== id)
      return jsonResponse(res, 204, {})
    }
  }

  if (pathname === '/api/v1/storage/policies') {
    return jsonResponse(res, 200, { items: policiesForUser() })
  }

  if (pathname === '/api/v1/nodes' && req.method === 'GET') {
    const parent = Number(params.get('parent') ?? 0)
    const limit = Number(params.get('limit') ?? 50)
    const offset = Number(params.get('offset') ?? 0)
    return jsonResponse(res, 200, nodePage(parent, limit, offset))
  }

  if (pathname === '/api/v1/nodes' && req.method === 'POST') {
    const parent = Number(body.parent)
    const name = typeof body.name === 'string' ? body.name : ''
    if (body.storage_policy_id !== undefined && parent !== 0) {
      return apiError(res, 400, 'invalid_target', '存储位置只对根目录生效')
    }
    if (body.storage_policy_id !== undefined) {
      const policy = state.policies.find((item) => item.id === body.storage_policy_id)
      if (!policy || !policy.enabled) {
        return apiError(res, 400, 'invalid_target', '所选存储位置不可用，请刷新后重试')
      }
    }
    const siblings = state.nodes[parent] ?? []
    if (siblings.some((node) => node.name === name)) {
      return apiError(res, 409, 'name_conflict', '同名文件夹已存在')
    }
    const now = new Date().toISOString()
    const node = {
      id: state.nextNodeId++,
      parent_id: parent,
      kind: body.kind === 'file' ? 'file' : 'dir',
      name,
      size: 0,
      revision: 1,
      created_at: now,
      updated_at: now,
    }
    state.nodes[parent] = [node, ...siblings]
    return jsonResponse(res, 201, node)
  }

  const copyMatch = pathname.match(/^\/api\/v1\/nodes\/(\d+)\/copy$/)
  if (copyMatch && req.method === 'POST') {
    return apiError(res, 409, 'conflict', '不支持跨存储复制', 'cross_policy_operation')
  }

  return apiError(res, 404, 'not_found', `mock 未实现：${req.method} ${pathname}`)
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
}

function serveStatic(res, pathname) {
  const safePath = path.normalize(pathname).replace(/^([/\\])+/, '')
  let filePath = path.join(DIST_ROOT, safePath)
  if (!filePath.startsWith(DIST_ROOT)) {
    res.writeHead(403)
    return res.end('forbidden')
  }
  if (!existsSync(filePath) || statSync(filePath).isDirectory()) filePath = path.join(DIST_ROOT, 'index.html')
  const ext = path.extname(filePath).toLowerCase()
  const bytes = readFileSync(filePath)
  res.writeHead(200, {
    'content-type': MIME[ext] ?? 'application/octet-stream',
    'cache-control': 'no-store',
    'content-length': bytes.length,
  })
  res.end(bytes)
}

function startMockServer() {
  const server = http.createServer((req, res) => {
    const url = req.url ?? '/'
    if (url.startsWith('/api/')) {
      handleApi(req, res, url).catch((error) => {
        apiError(res, 500, 'internal', `mock 内部错误：${error.message}`)
      })
      return
    }
    if (url === '/health') {
      res.writeHead(200, { 'content-type': 'text/plain' })
      return res.end('ok')
    }
    serveStatic(res, requestPath(url))
  })

  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve(server))
  })
}

/* ------------------------------------------------------------------- CDP */
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

async function freePort() {
  return await new Promise((resolve, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address()
      probe.close(() => resolve(address.port))
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
  const listeners = new Map()
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
    for (const listener of listeners.get(message.method) ?? []) listener(message.params, message.sessionId)
  }

  function send(method, params = {}, sessionId) {
    return new Promise((resolve, reject) => {
      const id = nextId++
      pending.set(id, { resolve, reject })
      socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
    })
  }

  function on(method, listener) {
    const list = listeners.get(method) ?? []
    list.push(listener)
    listeners.set(method, list)
    return () => {
      const next = (listeners.get(method) ?? []).filter((item) => item !== listener)
      listeners.set(method, next)
    }
  }

  return { send, on, pageErrors }
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

function check(name, ok, detail = '') {
  checks.push({ name, ok, detail })
  const mark = ok ? 'PASS' : 'FAIL'
  const extra = detail ? ` — ${detail}` : ''
  console.log(`${mark} ${name}${extra}`)
  if (!ok) process.exitCode = 1
}

function summarizeFailures() {
  const failed = checks.filter((item) => !item.ok)
  if (failed.length === 0) return
  console.error('\n失败项：')
  for (const item of failed) console.error(` - ${item.name}${item.detail ? ` — ${item.detail}` : ''}`)
}

async function main() {
  mkdirSync(SCREENSHOT_DIR, { recursive: true })
  const server = await startMockServer()
  const port = server.address().port
  const baseUrl = `http://127.0.0.1:${port}`

  const chromePath = await resolveChrome()
  const cdpPort = await freePort()
  const userDataDir = mkdtempSync(path.join(os.tmpdir(), 'rapig-mock-'))
  const browser = await launchBrowser(cdpPort, userDataDir, chromePath)
  const socket = await connectWebSocket(browser.wsUrl)
  const cdp = createCdp(socket)

  try {
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
    await cdp.send('Page.enable', {}, sessionId)
    await cdp.send('Runtime.enable', {}, sessionId)
    await cdp.send('Network.enable', {}, sessionId)

    // The SPA reads a CSRF cookie for mutating requests; give it one up front.
    await cdp.send(
      'Network.setCookie',
      { name: 'rapig_csrf', value: 'mock-csrf-token', domain: '127.0.0.1', path: '/', sameSite: 'Lax' },
      sessionId,
    )

    const evaluate = async (expression) => {
      const result = await cdp.send(
        'Runtime.evaluate',
        { expression, awaitPromise: true, returnByValue: true, userGesture: true },
        sessionId,
      )
      if (result.exceptionDetails) {
        throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text ?? 'evaluate 失败')
      }
      return result.result.value
    }

    const waitFor = async (expression, label, timeout = 8000) => {
      const started = Date.now()
      while (Date.now() - started < timeout) {
        try {
          if (await evaluate(`Boolean(${expression})`)) return
        } catch {
          // execution context can vanish during navigation; retry
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

    const clickSelector = async (selector) => {
      const ok = await evaluate(
        `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return false; el.click(); return true })()`,
      )
      if (!ok) throw new Error(`找不到可点击元素：${selector}`)
    }

    const clickText = async (selector, label) => {
      const result = await evaluate(
        `(() => {
          const items = [...document.querySelectorAll(${JSON.stringify(selector)})]
          const target = items.find((el) => el.textContent.trim() === ${JSON.stringify(label)})
          if (!target) return false
          target.click()
          return true
        })()`,
      )
      if (!result) throw new Error(`找不到文本为「${label}」的元素：${selector}`)
    }

    const rowButton = async (rowName, label) => {
      return await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.page table tbody tr')].find(
            (tr) => tr.querySelector('.cell-name')?.textContent.trim() === ${JSON.stringify(rowName)},
          )
          if (!row) return 'no-row'
          const button = [...row.querySelectorAll('button')].find(
            (btn) => btn.textContent.trim() === ${JSON.stringify(label)},
          )
          if (!button) return 'no-button'
          button.click()
          return 'ok'
        })()`,
      )
    }

    const setInput = async (selector, value) => {
      const ok = await evaluate(
        `(() => {
          const el = document.querySelector(${JSON.stringify(selector)})
          if (!el) return false
          const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set
          setter.call(el, ${JSON.stringify(value)})
          el.dispatchEvent(new Event('input', { bubbles: true }))
          return true
        })()`,
      )
      if (!ok) throw new Error(`找不到输入框：${selector}`)
    }

    const setSelect = async (selector, value) => {
      const ok = await evaluate(
        `(() => {
          const el = document.querySelector(${JSON.stringify(selector)})
          if (!el) return false
          el.value = ${JSON.stringify(String(value))}
          el.dispatchEvent(new Event('change', { bubbles: true }))
          return true
        })()`,
      )
      if (!ok) throw new Error(`找不到下拉框：${selector}`)
    }

    const textOf = async (selector) =>
      await evaluate(
        `(() => { const el = document.querySelector(${JSON.stringify(selector)}); return el ? el.textContent.trim() : null })()`,
      )

    const rowNames = async () =>
      await evaluate(
        `[...document.querySelectorAll('.page table tbody tr')].map((tr) => tr.querySelector('.cell-name')?.textContent.trim() ?? '')`,
      )

    const lastRequest = (method, matcher) =>
      [...apiLog].reverse().find((entry) => entry.method === method && matcher(entry.pathname))

    const waitToast = async (fragment, timeout = 6000) => {
      await waitFor(
        `[...document.querySelectorAll('.toast-message')].some((el) => el.textContent.includes(${JSON.stringify(fragment)}))`,
        `toast 包含「${fragment}」`,
        timeout,
      )
    }

    const waitForRequest = async (matcher, label, timeout = 8000) => {
      const started = Date.now()
      while (Date.now() - started < timeout) {
        if (apiLog.some(matcher)) return
        await sleep(50)
      }
      throw new Error(`等待请求超时：${label}`)
    }

    const screenshot = async (name) => {
      const { data } = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
      const file = path.join(SCREENSHOT_DIR, name)
      writeFileSync(file, Buffer.from(data, 'base64'))
      return file
    }

    /* ------------------------------------------------ 1. 管理端存储列表 */
    await navigate('/admin/storage')
    await waitFor(`document.querySelectorAll('.page table tbody tr .cell-name').length === 3`, '存储行渲染')

    check('页面标题为「存储」', (await textOf('.page-title')) === '存储')
    check('副标题为「内容寻址对象的本地存储根」', (await textOf('.page-sub')) === '内容寻址对象的本地存储根')
    check('列表渲染 3 条存储', (await rowNames()).join(',') === '主存储,备份盘,归档', (await rowNames()).join(','))
    check(
      '根路径用等宽 code 渲染',
      (await evaluate(`document.querySelector('.page table tbody tr .cell-path code.path')?.textContent.trim()`)) ===
        'D:\\rapig\\storage',
    )
    check(
      '停用存储显示「停用」chip',
      (await evaluate(
        `[...document.querySelectorAll('.page table tbody tr')].find((tr) => tr.querySelector('.cell-name')?.textContent.trim() === '归档')?.querySelector('.status-badge')?.textContent.trim()`,
      )) === '停用',
    )
    check(
      '只有默认存储带「默认」chip',
      (await evaluate(
        `[...document.querySelectorAll('.page table tbody tr')].filter((tr) => tr.querySelector('.default-chip')).map((tr) => tr.querySelector('.cell-name')?.textContent.trim()).join(',')`,
      )) === '主存储',
    )
    check(
      '已用容量十进制格式化（1.29 GB）',
      (await evaluate(
        `[...document.querySelectorAll('.page table tbody tr')].find((tr) => tr.querySelector('.cell-name')?.textContent.trim() === '主存储')?.querySelectorAll('td')[4]?.textContent.trim()`,
      )) === '1.29 GB',
    )
    check(
      '节点数 / 文件数列',
      (await evaluate(
        `[...document.querySelectorAll('.page table tbody tr')].find((tr) => tr.querySelector('.cell-name')?.textContent.trim() === '主存储')?.querySelectorAll('td')[5]?.textContent.trim()`,
      )) === '3 / 2',
    )
    check(
      '导航含「存储」且指向 /admin/storage',
      (await evaluate(
        `[...document.querySelectorAll('a.nav-item')].some((a) => a.getAttribute('href') === '/admin/storage' && a.textContent.trim() === '存储')`,
      )) === true,
    )
    check('存在「新建存储」按钮', (await textOf('.page-actions .btn-primary')) === '新建存储')

    /* -------------------------------------------- 2. 新建对话框与校验 */
    await clickText('.page-actions button', '新建存储')
    await waitFor(`document.querySelector('#create-storage-title')?.textContent.trim() === '新建存储'`, '创建对话框')
    check(
      '对话框说明检查路径存在/可写/不重叠',
      (await textOf('.dialog-note')) === '保存时会检查路径存在、可写、不与其它策略重叠。',
    )

    await clickText('.dialog .dialog-actions button', '创建')
    await sleep(120)
    const emptyErrors = await evaluate(
      `[...document.querySelectorAll('.dialog .field-error')].map((el) => el.textContent.trim())`,
    )
    check('空表单显示名称与根路径必填错误', emptyErrors.includes('请填写存储名称') && emptyErrors.includes('请填写根路径'), emptyErrors.join('|'))

    await setInput('#storage-name', '归档盘2')
    await setInput('#storage-root', 'storage2')
    await clickText('.dialog .dialog-actions button', '创建')
    await sleep(120)
    check(
      '相对路径被拒绝（根路径必须是绝对路径）',
      (await evaluate(`[...document.querySelectorAll('.dialog .field-error')].map((el) => el.textContent.trim())`)).includes(
        '根路径必须是绝对路径',
      ),
    )

    await setInput('#storage-root', 'E:\\rapig\\storage2')
    await clickText('.dialog .dialog-actions button', '创建')
    await waitFor(`document.querySelector('#create-storage-title') === null`, '创建对话框关闭')
    const createRequest = lastRequest('POST', (pathname) => pathname === '/api/v1/admin/storage')
    check(
      '创建请求体符合契约',
      createRequest !== undefined &&
        JSON.stringify(createRequest.body) ===
          JSON.stringify({ name: '归档盘2', root_path: 'E:\\rapig\\storage2', enabled: true, is_default: false }),
      createRequest ? JSON.stringify(createRequest.body) : '无请求',
    )
    await waitFor(`document.querySelectorAll('.page table tbody tr .cell-name').length === 4`, '新存储行出现')
    check('新建存储出现在列表中', (await rowNames()).includes('归档盘2'))

    /* ---------------------------------------------- 3. 编辑根路径只读 */
    check('默认存储行有「编辑」入口', (await rowButton('主存储', '编辑')) === 'ok')
    await waitFor(`document.querySelector('#edit-storage-title') !== null`, '编辑对话框')
    await sleep(80)
    check(
      '已有数据的存储根路径只读',
      (await evaluate(`document.querySelector('#storage-edit-root')?.readOnly === true`)) === true,
    )
    check(
      '只读提示「该存储已有数据，不能修改根路径」',
      (await evaluate(`[...document.querySelectorAll('.dialog .field-hint')].map((el) => el.textContent.trim())`)).includes(
        '该存储已有数据，不能修改根路径',
      ),
    )
    await clickText('.dialog .dialog-actions button', '取消')
    await waitFor(`document.querySelector('#edit-storage-title') === null`, '编辑对话框关闭')

    check('空存储行有「编辑」入口', (await rowButton('备份盘', '编辑')) === 'ok')
    await waitFor(`document.querySelector('#edit-storage-title') !== null`, '编辑对话框（空存储）')
    await sleep(80)
    check(
      '空存储根路径可编辑',
      (await evaluate(`document.querySelector('#storage-edit-root')?.readOnly === false`)) === true,
    )
    await clickText('.dialog .dialog-actions button', '取消')
    await waitFor(`document.querySelector('#edit-storage-title') === null`, '编辑对话框（空存储）关闭')

    /* ------------------------------------------------------ 4. 删除 */
    check('删除入口存在', (await rowButton('归档', '删除')) === 'ok')
    await waitFor(`document.querySelector('#confirm-title') !== null`, '删除确认框')
    check(
      '删除确认文案说明只有空存储可删',
      (await textOf('.dialog-message')).includes('确定要删除存储「归档」吗？') &&
        (await textOf('.dialog-detail')).includes('仅当该存储下没有任何节点与文件时才能删除'),
    )
    await clickSelector('.dialog .btn-danger')
    await waitFor(`!([...document.querySelectorAll('.page table tbody tr .cell-name')].some((el) => el.textContent.trim() === '归档'))`, '归档行消失')
    check('空存储删除成功并移除行', !(await rowNames()).includes('归档'))

    check('默认存储的删除入口也可点击', (await rowButton('主存储', '删除')) === 'ok')
    await waitFor(`document.querySelector('#confirm-title') !== null`, '删除默认存储确认框')
    await clickSelector('.dialog .btn-danger')
    await waitToast('默认存储不能删除')
    check(
      '删除默认存储 409 时 toast 显示后端 reason',
      (await evaluate(`[...document.querySelectorAll('.toast-message')].map((el) => el.textContent.trim()).join('|')`)).includes(
        '默认存储不能删除，请先把其它存储设为默认',
      ),
    )

    /* -------------------------------------------- 5. 停用 / 启用 */
    check('停用按钮存在', (await rowButton('备份盘', '停用')) === 'ok')
    await waitFor(
      `[...document.querySelectorAll('.page table tbody tr')].find((tr) => tr.querySelector('.cell-name')?.textContent.trim() === '备份盘')?.querySelector('.status-badge')?.textContent.trim() === '停用'`,
      '备份盘变为停用',
    )
    const patchRequest = lastRequest('PATCH', (pathname) => pathname === '/api/v1/admin/storage/2')
    check(
      '停用请求体为 {enabled:false}',
      patchRequest !== undefined && patchRequest.body.enabled === false,
      patchRequest ? JSON.stringify(patchRequest.body) : '无请求',
    )

    /* ------------------------------------ 6. 根目录选择存储位置 */
    await navigate('/files')
    await waitForRequest((entry) => entry.method === 'GET' && entry.url.includes('/api/v1/nodes?') && entry.url.includes('parent=0'), '根目录列表')
    await sleep(200)
    await clickText('button', '新建文件夹')
    await waitFor(`document.querySelector('#new-folder-name') !== null`, '新建文件夹对话框')
    await waitFor(`document.querySelector('#new-folder-storage') !== null`, '存储位置下拉出现')
    check(
      '根目录显示存储位置下拉（2 个启用策略）',
      (await evaluate(`document.querySelectorAll('#new-folder-storage option').length`)) === 2,
    )
    check(
      '存储位置默认选中 is_default 策略',
      (await evaluate(`document.querySelector('#new-folder-storage')?.value`)) === '1' &&
        (await evaluate(`document.querySelector('#new-folder-storage option:checked')?.textContent.trim()`)) === '主存储（默认）',
    )
    await setSelect('#new-folder-storage', 4)
    await setInput('#new-folder-name', '失效目录')
    const selectedPolicy = state.policies.find((policy) => policy.id === 4)
    selectedPolicy.enabled = false
    await clickText('.dialog .dialog-actions button', '创建')
    await waitFor(`document.querySelector('.dialog .submit-error') !== null`, 'invalid_target 提示')
    check(
      '所选存储被停用后返回 400 invalid_target 并显示 message',
      (await textOf('.dialog .submit-error')) === '所选存储位置不可用，请刷新后重试',
    )
    selectedPolicy.enabled = true

    await setInput('#new-folder-name', '新目录')
    await clickText('.dialog .dialog-actions button', '创建')
    await waitFor(`document.querySelector('#new-folder-name') === null`, '新建文件夹对话框关闭')
    const folderRequest = lastRequest('POST', (pathname) => pathname === '/api/v1/nodes')
    check(
      '根目录创建携带 storage_policy_id',
      folderRequest !== undefined && folderRequest.body.storage_policy_id === 4 && folderRequest.body.parent === 0,
      folderRequest ? JSON.stringify(folderRequest.body) : '无请求',
    )

    /* 非根目录：不显示、不传 */
    await navigate('/files/2')
    await waitForRequest((entry) => entry.method === 'GET' && entry.url.includes('/api/v1/nodes?') && entry.url.includes('parent=2'), '子目录列表')
    await sleep(200)
    await clickText('button', '新建文件夹')
    await waitFor(`document.querySelector('#new-folder-name') !== null`, '非根目录新建文件夹对话框')
    await sleep(250)
    check('非根目录不显示存储位置下拉', (await evaluate(`document.querySelector('#new-folder-storage') === null`)) === true)
    await setInput('#new-folder-name', '子目录')
    await clickText('.dialog .dialog-actions button', '创建')
    await waitFor(`document.querySelector('#new-folder-name') === null`, '非根目录新建对话框关闭')
    const childRequest = lastRequest('POST', (pathname) => pathname === '/api/v1/nodes')
    check(
      '非根目录创建不携带 storage_policy_id',
      childRequest !== undefined && childRequest.body.parent === 2 && !('storage_policy_id' in childRequest.body),
      childRequest ? JSON.stringify(childRequest.body) : '无请求',
    )

    /* -------------------------------------- 7. 跨策略复制 toast */
    await navigate('/files')
    await waitForRequest((entry) => entry.method === 'GET' && entry.url.includes('/api/v1/nodes?') && entry.url.includes('parent=0'), '根目录列表')
    await sleep(250)
    await clickSelector('button[aria-label="「a.txt」更多操作"]')
    await waitFor(`document.querySelector('.row-menu') !== null`, '文件操作菜单')
    await clickText('.row-menu-item', '复制')
    await waitToast('不支持跨存储移动/复制')
    check(
      '跨策略复制失败 toast 映射为「不支持跨存储移动/复制」',
      (await evaluate(`[...document.querySelectorAll('.toast-message')].map((el) => el.textContent.trim()).join('|')`)).includes(
        '不支持跨存储移动/复制',
      ),
    )

    /* ------------------------------------------ 8. 空态 / 错误态 / 390px */
    const savedPolicies = state.policies.map((policy) => ({ ...policy }))
    state.policies = []
    await navigate('/admin/storage')
    await waitFor(`document.querySelector('.state-title')?.textContent.trim() === '还没有存储策略'`, '空态')
    check(
      '无存储时显示空态与新建入口',
      (await evaluate(
        `[...document.querySelectorAll('.state button')].some((btn) => btn.textContent.trim() === '新建存储')`,
      )) === true,
    )
    state.policies = savedPolicies

    state.failStorageList = true
    await navigate('/admin/storage')
    await waitFor(`document.querySelector('.state[role="alert"]') !== null`, '错误态')
    check(
      '加载失败显示错误态与后端 message',
      (await evaluate(
        `document.querySelector('.state[role="alert"]')?.textContent.includes('存储服务暂时不可用')`,
      )) === true,
    )
    state.failStorageList = false
    await clickText('.state[role="alert"] button', '重试')
    await waitFor(`document.querySelectorAll('.page table tbody tr .cell-name').length === 3`, '重试后数据恢复')
    check('重试后恢复存储列表', (await rowNames()).length === 3)

    await cdp.send(
      'Emulation.setDeviceMetricsOverride',
      { width: 390, height: 844, deviceScaleFactor: 1, mobile: true, screenWidth: 390, screenHeight: 844 },
      sessionId,
    )
    await navigate('/admin/storage')
    await waitFor(`document.querySelectorAll('.page table tbody tr .cell-name').length === 3`, '390px 存储行渲染')
    const overflow = await evaluate(
      `({ doc: document.documentElement.scrollWidth, body: document.body.scrollWidth, view: window.innerWidth })`,
    )
    check('390px 无页面横向溢出', overflow.doc <= overflow.view && overflow.body <= overflow.view, JSON.stringify(overflow))
    check(
      '表格位于横向滚动容器内',
      (await evaluate(
        `(() => { const wrap = document.querySelector('.table-scroll'); const table = wrap?.querySelector('table'); if (!wrap || !table) return false; return getComputedStyle(wrap).overflowX === 'auto' && table.scrollWidth > wrap.clientWidth })()`,
      )) === true,
    )
    const mobileShot = await screenshot('web-storage-390.png')
    console.log(`截图（390px）：${mobileShot}`)

    await cdp.send(
      'Emulation.setDeviceMetricsOverride',
      { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false, screenWidth: 1280, screenHeight: 900 },
      sessionId,
    )
    await sleep(200)
    const desktopShot = await screenshot('web-storage-1280.png')
    console.log(`截图（1280px）：${desktopShot}`)

    /* ------------------------------------------ 9. 非 admin 路由守卫 */
    state.role = 'user'
    await navigate('/admin/storage')
    await waitFor(`location.pathname === '/files'`, '非 admin 跳转 /files')
    check('非 admin 访问 /admin/storage 被重定向到 /files', (await evaluate(`location.pathname`)) === '/files')
    state.role = 'admin'

    /* ------------------------------------------------------ 收尾 */
    if (cdp.pageErrors.length > 0) {
      check('页面无未捕获异常', false, cdp.pageErrors.slice(0, 3).join(' | '))
    } else {
      check('页面无未捕获异常', true)
    }

    summarizeFailures()
    const passed = checks.filter((item) => item.ok).length
    console.log(`\n结果：${passed}/${checks.length} 通过`)
    console.log(`截图目录：${SCREENSHOT_DIR}`)
    if (process.exitCode) process.exitCode = 1
  } finally {
    try {
      socket.close()
    } catch {
      // ignore
    }
    try {
      browser.child.kill()
    } catch {
      // ignore
    }
    await new Promise((resolve) => server.close(resolve))
    try {
      rmSync(userDataDir, { recursive: true, force: true })
    } catch {
      // Windows may still hold the profile open briefly; ignore.
    }
  }
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
