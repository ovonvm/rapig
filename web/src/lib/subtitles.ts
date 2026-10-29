/**
 * Client-side subtitle conversion, text only.
 *
 * Browsers only render WebVTT through <track>. SRT converts losslessly (timing
 * + text); ASS/SSA is reduced to its Dialogue lines (styling, positioning and
 * karaoke effects are dropped on purpose — the player shows plain subtitles).
 *
 * Nothing here touches the server: files are fetched through the normal
 * download endpoint and converted in the browser.
 */

const MAX_SUBTITLE_BYTES = 1024 * 1024

export interface SubtitleConversion {
  vtt: string
  /** Non-null when content lost formatting (ASS/SSA). */
  note: string | null
}

function pad(value: number, width = 2): string {
  return String(Math.max(0, Math.floor(value))).padStart(width, '0')
}

/** Milliseconds -> WebVTT timestamp `hh:mm:ss.mmm`. */
export function toVttTime(ms: number): string {
  const total = Math.max(0, Math.round(ms))
  const hours = Math.floor(total / 3_600_000)
  const minutes = Math.floor((total % 3_600_000) / 60_000)
  const seconds = Math.floor((total % 60_000) / 1000)
  const millis = total % 1000
  return `${pad(hours)}:${pad(minutes)}:${pad(seconds)}.${pad(millis, 3)}`
}

/** `01:02:03,456` / `1:02:03.45` -> milliseconds. */
function parseClock(raw: string): number | null {
  const match = raw.trim().match(/^(?:(\d+):)?(\d{1,2}):(\d{1,2})(?:[.,](\d{1,3}))?$/)
  if (!match) return null
  const hours = Number(match[1] ?? 0)
  const minutes = Number(match[2] ?? 0)
  const seconds = Number(match[3] ?? 0)
  const fraction = (match[4] ?? '').padEnd(3, '0').slice(0, 3)
  return ((hours * 60 + minutes) * 60 + seconds) * 1000 + Number(fraction)
}

function vttHeader(): string {
  return 'WEBVTT\n\n'
}

function cue(start: number, end: number, text: string): string {
  const safeEnd = end > start ? end : start + 900
  return `${toVttTime(start)} --> ${toVttTime(safeEnd)}\n${text}\n`
}

function stripMarkup(text: string): string {
  return text
    .replace(/\{[^}]*\}/g, '')
    .replace(/<\/?[a-zA-Z][^>]*>/g, '')
    .replace(/\s+$/g, '')
    .replace(/^\s+/g, '')
}

/** SRT -> WebVTT; timing and text are preserved. */
export function srtToVtt(input: string): string {
  const blocks = input.replace(/\r\n/g, '\n').split(/\n{2,}/)
  const cues: string[] = []
  for (const block of blocks) {
    const lines = block.split('\n').filter((line) => line.trim() !== '')
    if (lines.length === 0) continue
    const timingIndex = lines.findIndex((line) => line.includes('-->'))
    if (timingIndex < 0) continue
    const [rawStart, rawEnd] = (lines[timingIndex] ?? '').split('-->')
    const start = parseClock(rawStart ?? '')
    const end = parseClock(rawEnd ?? '')
    if (start === null || end === null) continue
    const text = stripMarkup(lines.slice(timingIndex + 1).join('\n'))
    if (!text) continue
    cues.push(cue(start, end, text))
  }
  return vttHeader() + cues.join('\n')
}

/** ASS/SSA -> WebVTT, Dialogue text only (no styles, no positioning). */
export function assToVtt(input: string): string {
  const lines = input.replace(/\r\n/g, '\n').split('\n')
  let inEvents = false
  let format: string[] = []
  const cues: Array<{ start: number; end: number; text: string }> = []

  for (const rawLine of lines) {
    const line = rawLine.trim()
    if (line.startsWith('[')) {
      inEvents = /^\[events\]/i.test(line)
      continue
    }
    if (!inEvents) continue
    if (!format.length && /^format\s*:/i.test(line)) {
      format = line
        .slice(line.indexOf(':') + 1)
        .split(',')
        .map((part) => part.trim().toLowerCase())
      continue
    }
    if (!/^dialogue\s*:/i.test(line)) continue
    const fields = line.slice(line.indexOf(':') + 1).split(',')
    if (format.length === 0) format = ['layer', 'start', 'end', 'style', 'name', 'marginl', 'marginr', 'marginv', 'effect', 'text']
    if (fields.length < format.length) continue
    // Text is the final column and may itself contain commas.
    const textIndex = format.indexOf('text')
    const tail = fields.splice(textIndex < 0 ? fields.length - 1 : textIndex)
    const text = stripMarkup(
      tail
        .join(',')
        .replace(/\\[Nn]/g, '\n')
        .replace(/\\h/g, ' '),
    )
    const start = parseClock(fields[format.indexOf('start')] ?? '')
    const end = parseClock(fields[format.indexOf('end')] ?? '')
    if (start === null || end === null || !text) continue
    cues.push({ start, end, text })
  }

  cues.sort((a, b) => a.start - b.start)
  return vttHeader() + cues.map((item) => cue(item.start, item.end, item.text)).join('\n')
}

/** Convert a sidecar subtitle file to WebVTT based on its extension. */
export function convertSubtitle(name: string, raw: string): SubtitleConversion {
  const ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase()
  if (ext === 'vtt') {
    const text = raw.replace(/\r\n/g, '\n')
    return { vtt: text.startsWith('WEBVTT') ? text : vttHeader() + text, note: null }
  }
  if (ext === 'srt') return { vtt: srtToVtt(raw), note: null }
  return { vtt: assToVtt(raw), note: '字幕样式已简化（仅保留台词与时间轴）' }
}

/** Guard used before fetching a subtitle sidecar. */
export function subtitleTooLarge(size: number): boolean {
  return size > MAX_SUBTITLE_BYTES
}
