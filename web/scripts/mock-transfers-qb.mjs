#!/usr/bin/env node
/**
 * M6.1 前端自验：qBittorrent 磁力离线下载入口。
 *
 * 不依赖后端、不新装依赖（与 scripts/mock-storage.mjs 同一套做法）：
 *  - 内置 mock HTTP 服务，按 docs/16-m6-plan.md 的契约返回固定数据；
 *  - 用 Playwright 缓存里的 headless Chromium 通过裸 CDP 驱动；
 *  - 静态资源直接读 `web/dist`（先执行 `pnpm run build`）。
 *
 * 用法（Node 需在 PATH 上）：
 *   node scripts/mock-transfers-qb.mjs
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
const state = {
  role: 'admin',
  qb: true,
  transfers: [
    {
      id: 't-http-1',
      parent_id: 0,
      kind: 'http',
      name: '备份.tar.gz',
      source_url: 'https://cdn.example.com/backup.tar.gz',
      size: 1048576,
      received_bytes: 524288,
      state: 'running',
      error: null,
      node_id: null,
      created_at: '2026-10-27T10:00:00Z',
      updated_at: '2026-10-27T10:05:00Z',
      started_at: '2026-10-27T10:00:10Z',
      finished_at: null,
    },
    {
      id: 't-qb-1',
      parent_id: 0,
      kind: 'qb',
      name: 'ubuntu-24.04.iso',
      source_url: 'magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567',
      size: 0,
      received_bytes: 0,
      state: 'paused',
      error: null,
      node_id: null,
      created_at: '2026-10-27T09:00:00Z',
      updated_at: '2026-10-27T09:30:00Z',
      started_at: '2026-10-27T09:00:05Z',
      finished_at: null,
    },
    {
      id: 't-http-2',
      parent_id: 0,
      kind: 'http',
      name: '说明.txt',
      source_url: 'https://files.example.net/readme.txt',
      size: 2048,
      received_bytes: 2048,
      state: 'completed',
      error: null,
      node_id: 900,
      created_at: '2026-10-26T08:00:00Z',
      updated_at: '2026-10-26T08:00:20Z',
      started_at: '2026-10-26T08:00:05Z',
      finished_at: '2026-10-26T08:00:20Z',
    },
  ],
  seq: 10,
}

const apiLog = []

const users = {
  admin: { id: 1, username: 'tester', role: 'admin', is_root: true, used_bytes: 0, quota_bytes: 0 },
}

const BTIH = /xt=urn:btih:([a-z2-7]{32}|[0-9a-f]{40})/i

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
    return new URL(rawUrl, 'http://127.0.0.1').pathname
  } catch {
    return '/'
  }
}

function transferJson(item) {
  return { ...item }
}

function readRawBody(req) {
  return new Promise((resolve) => {
    const chunks = []
    req.on('data', (chunk) => chunks.push(chunk))
    req.on('end', () => resolve(Buffer.concat(chunks)))
  })
}

/** Multipart `.torrent` upload: only the fields the UI checks are parsed. */
async function handleTorrentUpload(req, res) {
  const raw = await readRawBody(req)
  const text = raw.toString('latin1')
  const filename = /filename="([^"]*)"/.exec(text)?.[1] ?? ''
  const nameField = /name="name"\r\n\r\n([^\r\n]*)/.exec(text)?.[1]?.trim() ?? ''
  const parentField = /name="parent"\r\n\r\n(\d+)/.exec(text)?.[1] ?? '0'
  const body = { filename, name: nameField, parent: Number(parentField), size: raw.length }
  apiLog.push({ method: 'POST', pathname: '/api/v1/transfers/torrent', url: '/api/v1/transfers/torrent', body })

  if (!filename) {
    return sendError(res, 400, 'bad_request', 'no file', { reason: 'invalid_torrent' })
  }
  if (/broken/i.test(filename)) {
    return sendError(res, 400, 'bad_request', 'cannot parse torrent', { reason: 'invalid_torrent' })
  }
  if (/huge/i.test(filename)) {
    return sendError(res, 413, 'payload_too_large', 'torrent too large', { reason: 'torrent_too_large' })
  }
  if (!state.qb) {
    return sendError(res, 503, 'qbt_unavailable', 'qBittorrent is not configured')
  }

  const item = {
    id: `t-torrent-${state.seq++}`,
    parent_id: Number(parentField) || 0,
    kind: 'qb',
    name: nameField || filename.replace(/\.torrent$/i, ''),
    source_url: `torrent:${filename}`,
    size: 0,
    received_bytes: 0,
    state: 'pending',
    error: null,
    node_id: null,
    created_at: '2026-10-27T12:00:00Z',
    updated_at: '2026-10-27T12:00:00Z',
    started_at: null,
    finished_at: null,
  }
  state.transfers = [item, ...state.transfers]
  return sendJson(res, 201, { transfer: transferJson(item) })
}

async function handleApi(req, res, rawUrl) {
  const pathname = requestPath(rawUrl)
  const method = req.method ?? 'GET'
  if (pathname === '/api/v1/transfers/torrent' && method === 'POST') {
    return handleTorrentUpload(req, res)
  }
  const body = method === 'GET' || method === 'HEAD' ? {} : await readBody(req)
  apiLog.push({ method, pathname, url: rawUrl, body })

  if (pathname === '/api/v1/auth/me') {
    return sendJson(res, 200, { user: users[state.role === 'admin' ? 'admin' : 'admin'] })
  }
  if (pathname === '/api/v1/auth/registration') {
    return sendJson(res, 200, { enabled: false })
  }
  if (pathname === '/api/v1/auth/logout') {
    res.writeHead(204)
    return res.end()
  }
  if (pathname === '/api/v1/nodes' && method === 'GET') {
    return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  }
  if (pathname === '/api/v1/trash') {
    return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  }
  if (pathname === '/api/v1/shares') {
    return sendJson(res, 200, { items: [], total: 0, limit: 50, offset: 0 })
  }
  if (pathname === '/api/v1/transfers/qb/status') {
    return sendJson(res, 200, { enabled: state.qb })
  }
  if (pathname === '/api/v1/transfers' && method === 'GET') {
    return sendJson(res, 200, { items: state.transfers.map(transferJson), total: state.transfers.length, limit: 50, offset: 0 })
  }
  if (pathname === '/api/v1/transfers' && method === 'POST') {
    const kind = body.kind === 'qb' ? 'qb' : 'http'
    let magnet = ''
    if (kind === 'qb') {
      if (!state.qb) {
        return sendError(res, 503, 'qbt_unavailable', 'qBittorrent is not configured')
      }
      magnet = typeof body.magnet === 'string' ? body.magnet.trim() : ''
      if (!/^magnet:\?/i.test(magnet) || !BTIH.test(magnet)) {
        return sendError(res, 400, 'bad_request', 'invalid magnet', { reason: 'invalid_magnet' })
      }
    } else if (typeof body.url !== 'string' || !/^https?:\/\//i.test(body.url)) {
      return sendError(res, 400, 'bad_request', 'invalid url', { reason: 'invalid_url' })
    }

    const fallbackName = kind === 'qb' ? '磁力任务' : (String(body.url).split('/').pop() || 'file')
    const item = {
      id: `t-new-${state.seq++}`,
      parent_id: typeof body.parent === 'number' ? body.parent : 0,
      kind,
      name: typeof body.name === 'string' && body.name.trim() ? body.name.trim() : fallbackName,
      source_url: kind === 'qb' ? magnet : body.url,
      size: 0,
      received_bytes: 0,
      state: 'pending',
      error: null,
      node_id: null,
      created_at: '2026-10-27T12:00:00Z',
      updated_at: '2026-10-27T12:00:00Z',
      started_at: null,
      finished_at: null,
    }
    state.transfers = [item, ...state.transfers]
    return sendJson(res, 201, { transfer: transferJson(item) })
  }

  const action = /^\/api\/v1\/transfers\/([^/]+)\/(pause|resume)$/.exec(pathname)
  if (action && method === 'POST') {
    const item = state.transfers.find((entry) => entry.id === action[1])
    if (!item) return sendError(res, 404, 'not_found', 'no transfer')
    item.state = action[2] === 'pause' ? 'paused' : 'pending'
    res.writeHead(204)
    return res.end()
  }

  const one = /^\/api\/v1\/transfers\/([^/]+)$/.exec(pathname)
  if (one && method === 'DELETE') {
    state.transfers = state.transfers.filter((entry) => entry.id !== one[1])
    res.writeHead(204)
    return res.end()
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
  let filePath = path.join(DIST_ROOT, safePath)
  if (!filePath.startsWith(DIST_ROOT)) {
    res.writeHead(403)
    return res.end('forbidden')
  }
  if (!existsSync(filePath) || statSync(filePath).isDirectory()) filePath = path.join(DIST_ROOT, 'index.html')
  const bytes = readFileSync(filePath)
  res.writeHead(200, {
    'content-type': MIME[path.extname(filePath).toLowerCase()] ?? 'application/octet-stream',
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
    serveStatic(res, requestPath(url))
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

function check(name, ok, detail = '') {
  checks.push({ name, ok, detail })
  const mark = ok ? 'PASS' : 'FAIL'
  console.log(`${mark} ${name}${detail ? ` — ${detail}` : ''}`)
  if (!ok) process.exitCode = 1
}

async function main() {
  mkdirSync(SCREENSHOT_DIR, { recursive: true })
  const server = await startMockServer()
  const { port } = server.address()
  const baseUrl = `http://127.0.0.1:${port}`
  const chromePath = await resolveChrome()
  const cdpPort = await freePort()
  const userDataDir = mkdtempSync(path.join(os.tmpdir(), 'rapig-mock-qb-'))
  const browser = await launchBrowser(cdpPort, userDataDir, chromePath)
  const socket = await connectWebSocket(browser.wsUrl)
  const cdp = createCdp(socket)

  try {
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
    await cdp.send('Page.enable', {}, sessionId)
    await cdp.send('Runtime.enable', {}, sessionId)
    await cdp.send('Network.enable', {}, sessionId)
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

    const textOf = async (selector) =>
      await evaluate(
        `(() => { const el = document.querySelector(${JSON.stringify(selector)}); return el ? el.textContent.trim() : null })()`,
      )

    const rowNames = async () =>
      await evaluate(
        `[...document.querySelectorAll('.rows li.row .cell-name .name')].map((el) => el.textContent.trim())`,
      )

    const rowByName = async (name) =>
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === ${JSON.stringify(name)},
          )
          return row ? true : false
        })()`,
      )

    const clickRowButton = async (name, label) => {
      const ok = await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === ${JSON.stringify(name)},
          )
          if (!row) return 'no-row'
          const button = [...row.querySelectorAll('button')].find((btn) => btn.textContent.trim() === ${JSON.stringify(label)})
          if (!button) return 'no-button'
          button.click()
          return 'ok'
        })()`,
      )
      if (ok !== 'ok') throw new Error(`${name} 行找不到按钮 ${label}：${ok}`)
    }

    const lastRequest = (method, matcher) =>
      [...apiLog].reverse().find((entry) => entry.method === method && matcher(entry.pathname))

    const waitToast = async (fragment, timeout = 6000) => {
      await waitFor(
        `[...document.querySelectorAll('.toast-message')].some((el) => el.textContent.includes(${JSON.stringify(fragment)}))`,
        `toast 包含「${fragment}」`,
        timeout,
      )
    }

    const shot = async (name) => {
      const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
      // `send` resolves with the CDP result object, i.e. `{data}`.
      const { writeFileSync } = await import('node:fs')
      writeFileSync(path.join(SCREENSHOT_DIR, name), Buffer.from(result.data, 'base64'))
    }

    /* ---------------------------------------------------------- 列表基线 */
    await navigate('/transfers')
    await waitFor(`document.querySelectorAll('.rows li.row').length === 3`, '离线下载列表 3 行')

    check('列表渲染 3 条任务', (await rowNames()).length === 3, JSON.stringify(await rowNames()))
    check(
      'qb 任务带「磁力」标签',
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === 'ubuntu-24.04.iso',
          )
          return row?.querySelector('.kind-tag')?.textContent.trim() === '磁力'
        })()`,
      ),
    )
    check(
      'HTTP 任务没有「磁力」标签',
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === '备份.tar.gz',
          )
          return row?.querySelector('.kind-tag') == null
        })()`,
      ),
    )
    check(
      'qb 任务来源列显示「磁力链接」',
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === 'ubuntu-24.04.iso',
          )
          return row?.querySelector('.col-source')?.textContent.trim() === '磁力链接'
        })()`,
      ),
    )

    const statusRequest = lastRequest('GET', (p) => p === '/api/v1/transfers/qb/status')
    check('页面加载时请求了 qb 状态', Boolean(statusRequest))

    /* ---------------------------------------------------------- 创建对话框 */
    await clickText('.page-actions button', '新建任务')
    await waitFor(`document.querySelector('.dialog') !== null`, '新建任务对话框')
    check(
      '对话框显示三种链接类型',
      (await evaluate(`[...document.querySelectorAll('.mode-option')].map((el) => el.textContent.trim())`))?.join('|') ===
        'HTTP 链接|磁力链接|种子文件',
    )

    await clickText('.mode-option', '磁力链接')
    await waitFor(`document.querySelector('#offline-magnet') !== null`, '磁力输入框')
    check(
      '磁力模式隐藏 HTTP 输入框',
      (await evaluate(`document.querySelector('#offline-url') === null`)) === true,
    )

    const postsBefore = apiLog.filter((entry) => entry.method === 'POST' && entry.pathname === '/api/v1/transfers').length

    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    check('空磁力链接给出提示', (await textOf('#offline-link-error')) === '请输入磁力链接')

    await setInput('#offline-magnet', 'not-a-magnet')
    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    check('非法磁力链接给出提示', (await textOf('#offline-link-error')) === '磁力链接格式不正确')
    check(
      '非法输入没有发请求',
      apiLog.filter((entry) => entry.method === 'POST' && entry.pathname === '/api/v1/transfers').length === postsBefore,
    )

    await setInput('#offline-magnet', 'magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567')
    await setInput('#offline-name', '测试磁力')
    await sleep(300) // let the dialog enter transition finish
    await shot('web-transfers-qb-1280.png')

    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    await waitToast('已加入磁力下载')
    const createRequest = lastRequest('POST', (p) => p === '/api/v1/transfers')
    check(
      '创建请求带 kind=qb 与 magnet',
      Boolean(createRequest) &&
        createRequest.body.kind === 'qb' &&
        String(createRequest.body.magnet).startsWith('magnet:?xt=urn:btih:') &&
        createRequest.body.parent === 0,
      JSON.stringify(createRequest?.body ?? null),
    )
    await waitFor(`document.querySelectorAll('.rows li.row').length === 4`, '列表出现新磁力任务')
    check('新磁力任务带「磁力」标签', (await rowByName('测试磁力')) === true)
    check(
      '新任务标签文本正确',
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === '测试磁力',
          )
          return row?.querySelector('.kind-tag')?.textContent.trim() === '磁力'
        })()`,
      ),
    )

    /* ---------------------------------------------------------- 种子文件上传 */
    const setTorrentFile = async (name, size) => {
      await evaluate(`(() => {
        const input = document.querySelector('.torrent-input')
        if (!input) throw new Error('missing .torrent input')
        const dt = new DataTransfer()
        dt.items.add(new File([new Uint8Array(${size})], ${JSON.stringify(name)}, { type: 'application/x-bittorrent' }))
        input.files = dt.files
        input.dispatchEvent(new Event('change', { bubbles: true }))
        return 'ok'
      })()`)
    }

    await clickText('.page-actions button', '新建任务')
    await waitFor(`document.querySelector('.dialog') !== null`, '新建任务对话框')
    await clickText('.mode-option', '种子文件')
    await waitFor(`document.querySelector('.torrent-input') !== null`, '种子文件输入')
    check(
      '种子模式隐藏 URL / 磁力输入框',
      (await evaluate(`document.querySelector('#offline-url') === null && document.querySelector('#offline-magnet') === null`)) ===
        true,
    )

    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    check('未选文件时提示', (await textOf('#offline-file-error')) === '请选择种子文件')

    await setTorrentFile('note.txt', 64)
    check('非 .torrent 后缀给出提示', (await textOf('#offline-file-error')) === '请选择 .torrent 种子文件')

    await setTorrentFile('demo.torrent', 1200)
    await waitFor(`document.querySelector('.file-chip') !== null`, '种子文件回显')
    check(
      '种子文件回显文件名与大小',
      (await textOf('.file-chip')).includes('demo.torrent') && (await textOf('.file-chip')).includes('1.20 KB'),
      String(await textOf('.file-chip')),
    )

    await sleep(260)
    await shot('web-transfers-torrent-dialog.png')

    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    await waitToast('已加入种子下载')
    const torrentRequest = lastRequest('POST', (p) => p === '/api/v1/transfers/torrent')
    check(
      '种子上传请求携带文件名与 parent',
      Boolean(torrentRequest) && torrentRequest.body.filename === 'demo.torrent' && torrentRequest.body.parent === 0,
      JSON.stringify(torrentRequest?.body ?? null),
    )
    await waitFor(`document.querySelectorAll('.rows li.row').length >= 4`, '列表出现种子任务')
    check(
      '种子任务标记为「种子」',
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === 'demo',
          )
          return row?.querySelector('.kind-tag')?.textContent.trim() === '种子'
        })()`,
      ),
    )
    check(
      '种子任务来源列显示「种子文件」',
      await evaluate(
        `(() => {
          const row = [...document.querySelectorAll('.rows li.row')].find(
            (el) => el.querySelector('.cell-name .name')?.textContent.trim() === 'demo',
          )
          return row?.querySelector('.col-source')?.textContent.trim() === '种子文件'
        })()`,
      ),
    )
    await sleep(260)
    await shot('web-transfers-torrent-201.png')

    await clickText('.page-actions button', '新建任务')
    await waitFor(`document.querySelector('.dialog') !== null`, '新建任务对话框（错误分支）')
    await clickText('.mode-option', '种子文件')
    await setTorrentFile('broken.torrent', 64)
    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    await waitFor(`document.querySelector('.submit-error') !== null`, '种子解析错误')
    check(
      'invalid_torrent 映射为「种子文件无法解析」',
      (await textOf('.submit-error')) === '种子文件无法解析',
      String(await textOf('.submit-error')),
    )
    await sleep(260)
    await shot('web-transfers-torrent-invalid.png')

    await setTorrentFile('huge.torrent', 64)
    await evaluate(`document.querySelector('.dialog form button[type="submit"]').click()`)
    await waitFor(
      `document.querySelector('.submit-error')?.textContent.includes('20 MB') === true`,
      '种子体积上限错误',
    )
    check(
      'torrent_too_large 映射为 20 MB 上限文案',
      (await textOf('.submit-error')) === '种子文件不能超过 20 MB',
      String(await textOf('.submit-error')),
    )
    await sleep(260)
    await shot('web-transfers-torrent-413.png')
    await clickText('.dialog-actions button', '取消')
    await waitFor(`document.querySelector('.dialog') === null`, '关闭对话框')

    /* ---------------------------------------------------------- 删除确认文案 */
    await clickRowButton('测试磁力', '删除')
    await waitFor(`document.querySelector('[role="alertdialog"]') !== null`, '删除确认框')
    check(
      'qb 任务删除确认包含 qB 清理说明',
      String(await textOf('.dialog-detail')).includes('将同时停止并清理 qBittorrent 中的任务'),
      String(await textOf('.dialog-detail')),
    )
    await clickText('[role="alertdialog"] .dialog-actions .btn-danger', '删除')
    await waitFor(`document.querySelector('[role="alertdialog"]') === null`, '删除确认框关闭')
    check('qb 任务删除发出 DELETE', Boolean(lastRequest('DELETE', (p) => p === '/api/v1/transfers/t-new-10')))

    await clickRowButton('备份.tar.gz', '删除')
    await waitFor(`document.querySelector('[role="alertdialog"]') !== null`, '删除确认框')
    check(
      'HTTP 任务删除文案保持原样',
      (await textOf('.dialog-detail')) === '已下载完成的文件会保留在网盘中。',
    )
    await clickText('[role="alertdialog"] .dialog-actions .btn-secondary', '取消')
    await waitFor(`document.querySelector('[role="alertdialog"]') === null`, '取消删除')

    /* ---------------------------------------------------------- 390 种子对话框 */
    await cdp.send(
      'Emulation.setDeviceMetricsOverride',
      { width: 390, height: 844, deviceScaleFactor: 1, mobile: true },
      sessionId,
    )
    await sleep(200)
    await clickText('.page-actions button', '新建任务')
    await waitFor(`document.querySelector('.dialog') !== null`, '移动端新建任务对话框')
    await clickText('.mode-option', '种子文件')
    await setTorrentFile('mobile.torrent', 2048)
    await sleep(300)
    await shot('web-transfers-torrent-390.png')
    const dialogOverflow = await evaluate(
      `(() => { const d = document.querySelector('.dialog'); return d ? Math.max(0, d.scrollWidth - d.clientWidth) : -1 })()`,
    )
    check('390px 种子对话框无横向溢出', dialogOverflow === 0, String(dialogOverflow))
    await clickText('.dialog-actions button', '取消')
    await waitFor(`document.querySelector('.dialog') === null`, '关闭移动端对话框')

    /* ---------------------------------------------------------- qb 未配置 */
    state.qb = false
    await cdp.send('Page.reload', {}, sessionId)
    await sleep(200)
    await waitFor(`document.querySelectorAll('.rows li.row').length >= 2`, '重载后的任务列表')
    await clickText('.page-actions button', '新建任务')
    await waitFor(`document.querySelector('.dialog') !== null`, '新建任务对话框')
    await sleep(200)
    check('qb 未配置时隐藏磁力入口', (await evaluate(`document.querySelector('.mode-switch') === null`)) === true)
    check('qb 未配置时仍显示 HTTP 输入框', (await evaluate(`document.querySelector('#offline-url') !== null`)) === true)
    await clickText('.dialog-actions button', '取消')

    /* ---------------------------------------------------------- 390 视口 */
    await cdp.send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true }, sessionId)
    await cdp.send('Page.reload', {}, sessionId)
    await sleep(200)
    await waitFor(`document.querySelectorAll('.rows li.row').length >= 2`, '移动端任务列表')
    const overflow = await evaluate(
      `(() => { const root = document.documentElement; return Math.max(0, root.scrollWidth - root.clientWidth) })()`,
    )
    check('390px 无横向溢出', overflow === 0, String(overflow))
    await shot('web-transfers-qb-390.png')

    check('无页面异常', cdp.pageErrors.length === 0, JSON.stringify(cdp.pageErrors))

    const failed = checks.filter((item) => !item.ok)
    console.log(`\n${checks.length - failed.length}/${checks.length} checks passed`)
    if (failed.length > 0) {
      console.error('\n失败项：')
      for (const item of failed) console.error(` - ${item.name}${item.detail ? ` — ${item.detail}` : ''}`)
    }
  } finally {
    server.close()
    try {
      const { child } = browser
      child.kill()
    } catch {
      // ignore
    }
    socket.close()
    // Chromium keeps the profile locked for a moment after SIGTERM on Windows.
    for (let attempt = 0; attempt < 10; attempt += 1) {
      try {
        rmSync(userDataDir, { recursive: true, force: true })
        break
      } catch {
        await sleep(200)
      }
    }
  }
}

main().catch((error) => {
  console.error(`\n运行失败：${error.message}`)
  process.exit(1)
})
