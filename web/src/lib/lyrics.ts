export interface LyricLine {
  time: number
  text: string
}

/**
 * Parse an `.lrc` sidecar (one or more timestamps per line) into sorted lines.
 * Unknown/empty text lines are kept as gaps only when they have a timestamp.
 */
export function parseLrc(raw: string): LyricLine[] {
  const lines: LyricLine[] = []
  for (const rawLine of raw.replace(/\r\n/g, '\n').split('\n')) {
    const timestamps = [...rawLine.matchAll(/\[(\d{1,2}):(\d{1,2})(?:[.:](\d{1,3}))?\]/g)]
    if (timestamps.length === 0) continue
    const text = rawLine.replace(/\[[^\]]*\]/g, '').trim()
    for (const match of timestamps) {
      const minutes = Number(match[1] ?? 0)
      const seconds = Number(match[2] ?? 0)
      const fraction = (match[3] ?? '0').padEnd(3, '0').slice(0, 3)
      const time = (minutes * 60 + seconds) * 1000 + Number(fraction)
      lines.push({ time, text })
    }
  }
  return lines.sort((a, b) => a.time - b.time)
}

/** Index of the active lyric line for a playback position in milliseconds. */
export function activeLyricIndex(lines: LyricLine[], timeMs: number): number {
  let index = -1
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]
    if (line && line.time <= timeMs) index = i
    else break
  }
  return index
}
