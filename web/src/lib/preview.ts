export type PreviewKind = 'image' | 'video' | 'audio' | 'text' | 'pdf' | null

const IMAGE_EXT = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'avif', 'ico'])
const VIDEO_EXT = new Set(['mp4', 'webm', 'ogv', 'ogg', 'mov', 'm4v', 'mkv'])
const AUDIO_EXT = new Set(['mp3', 'wav', 'ogg', 'oga', 'm4a', 'aac', 'flac', 'opus'])
const TEXT_EXT = new Set([
  'txt',
  'md',
  'markdown',
  'log',
  'json',
  'csv',
  'tsv',
  'go',
  'js',
  'mjs',
  'cjs',
  'ts',
  'tsx',
  'jsx',
  'css',
  'scss',
  'html',
  'htm',
  'xml',
  'yaml',
  'yml',
  'ini',
  'conf',
  'toml',
  'sh',
  'sql',
])
const PDF_EXT = new Set(['pdf'])

/** Extension of a filename (lowercased), ignoring dotfiles like `.gitignore`. */
function extensionOf(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot < 0 || dot === name.length - 1) return ''
  return name.slice(dot + 1).toLowerCase()
}

/**
 * Which preview surface fits this file. The extension wins; when it is unknown
 * a MIME type (when the backend ever exposes one) is used as a fallback.
 */
export function previewKind(name: string, mime?: string | null): PreviewKind {
  const ext = extensionOf(name)
  if (IMAGE_EXT.has(ext)) return 'image'
  if (VIDEO_EXT.has(ext)) return 'video'
  if (AUDIO_EXT.has(ext)) return 'audio'
  if (PDF_EXT.has(ext)) return 'pdf'
  if (TEXT_EXT.has(ext)) return 'text'

  const type = (mime ?? '').toLowerCase().split(';')[0]?.trim() ?? ''
  if (!type) return null
  if (type.startsWith('image/') && type !== 'image/svg+xml') return 'image'
  if (type.startsWith('video/')) return 'video'
  if (type.startsWith('audio/')) return 'audio'
  if (type === 'application/pdf') return 'pdf'
  if (
    type.startsWith('text/') ||
    type === 'application/json' ||
    type === 'application/xml' ||
    type === 'application/javascript' ||
    type === 'application/x-yaml' ||
    type === 'application/x-sh'
  ) {
    return 'text'
  }
  return null
}

/** Short label shown next to the filename in the preview bar. */
export function previewKindLabel(kind: PreviewKind): string {
  switch (kind) {
    case 'image':
      return '图片'
    case 'video':
      return '视频'
    case 'audio':
      return '音频'
    case 'text':
      return '文本'
    case 'pdf':
      return 'PDF'
    default:
      return '文件'
  }
}

export interface TextPreview {
  /** Decoded text; empty when the payload looked binary. */
  text: string
  /** True when only the first `maxBytes` were returned. */
  truncated: boolean
  /** True when NUL bytes / too many control characters were found. */
  binary: boolean
  /** Decoding failures are reported instead of throwing. */
  error?: string
}

/** Bytes that make a payload look like a binary blob in the first 512 bytes. */
function looksBinary(bytes: Uint8Array): boolean {
  const sample = Math.min(bytes.length, 512)
  if (sample === 0) return false

  let control = 0
  for (let index = 0; index < sample; index += 1) {
    const byte = bytes[index] ?? 0
    if (byte === 0) return true
    // Allow tab, LF, CR and the usual printable ranges; count other controls.
    if (byte < 0x09 || (byte > 0x0d && byte < 0x20) || byte === 0x7f) control += 1
  }
  return control / sample > 0.1
}

/**
 * Fetch the beginning of a text file through the normal content endpoint
 * (Range keeps huge files cheap) and decode it as UTF-8 with a GBK fallback.
 */
export async function fetchTextPreview(url: string, maxBytes = 1024 * 1024): Promise<TextPreview> {
  let response: Response
  try {
    response = await fetch(url, {
      headers: { Range: `bytes=0-${maxBytes - 1}` },
      credentials: 'same-origin',
    })
  } catch {
    return { text: '', truncated: false, binary: false, error: '无法加载文件内容，请检查网络后重试' }
  }

  if (!response.ok && response.status !== 206) {
    return { text: '', truncated: false, binary: false, error: '无法加载文件内容，请稍后重试' }
  }

  let buffer: ArrayBuffer
  try {
    buffer = await response.arrayBuffer()
  } catch {
    return { text: '', truncated: false, binary: false, error: '读取文件内容失败，请稍后重试' }
  }

  const bytes = new Uint8Array(buffer).subarray(0, maxBytes)
  const truncated = buffer.byteLength >= maxBytes || bytes.length >= maxBytes

  if (looksBinary(bytes)) return { text: '', truncated, binary: true }

  try {
    const text = new TextDecoder('utf-8', { fatal: false }).decode(bytes)
    if (!text.includes('\uFFFD')) return { text, truncated, binary: false }
  } catch {
    // Fall through to the legacy encoding.
  }

  try {
    const text = new TextDecoder('gbk', { fatal: false }).decode(bytes)
    return { text, truncated, binary: false }
  } catch {
    return { text: '', truncated, binary: false, error: '无法识别文件编码，请下载后查看' }
  }
}
