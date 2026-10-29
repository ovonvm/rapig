import { defineStore } from 'pinia'

/** Playback modes exposed by the audio player. */
export type PlayMode = 'list' | 'one' | 'shuffle'

interface SavedPosition {
  position: number
  duration: number
  updatedAt: number
}

interface Persisted {
  volume: number
  muted: boolean
  mode: PlayMode
  speed: number
  showSubtitles: boolean
  positions: Record<string, SavedPosition>
}

const STORAGE_KEY = 'rapig.player.v1'
const MAX_POSITIONS = 200
const DEFAULT: Persisted = {
  volume: 1,
  muted: false,
  mode: 'list',
  speed: 1,
  showSubtitles: false,
  positions: {},
}

function load(): Persisted {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { ...DEFAULT, positions: {} }
    const parsed = JSON.parse(raw) as Partial<Persisted>
    return {
      volume: typeof parsed.volume === 'number' ? Math.min(1, Math.max(0, parsed.volume)) : DEFAULT.volume,
      muted: parsed.muted === true,
      mode: parsed.mode === 'one' || parsed.mode === 'shuffle' ? parsed.mode : 'list',
      speed: typeof parsed.speed === 'number' && parsed.speed > 0 ? parsed.speed : 1,
      showSubtitles: parsed.showSubtitles === true,
      positions: parsed.positions && typeof parsed.positions === 'object' ? parsed.positions : {},
    }
  } catch {
    return { ...DEFAULT, positions: {} }
  }
}

/** Player preferences + resume positions (best effort, localStorage). */
export const usePlayerStore = defineStore('player', {
  state: () => load(),
  actions: {
    persist(): void {
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(this.$state))
      } catch {
        // Storage can be full/disabled; playback must keep working.
      }
    },
    setVolume(volume: number): void {
      this.volume = Math.min(1, Math.max(0, volume))
      this.muted = this.volume === 0
      this.persist()
    },
    setMuted(muted: boolean): void {
      this.muted = muted
      this.persist()
    },
    setMode(mode: PlayMode): void {
      this.mode = mode
      this.persist()
    },
    setSpeed(speed: number): void {
      this.speed = speed
      this.persist()
    },
    setShowSubtitles(show: boolean): void {
      this.showSubtitles = show
      this.persist()
    },
    /** Remember a position only for meaningful, unfinished playback. */
    rememberPosition(nodeId: number, position: number, duration: number): void {
      if (!Number.isFinite(position) || !Number.isFinite(duration) || duration < 30) return
      if (position < 10) return
      if (position >= duration - 5) {
        this.clearPosition(nodeId)
        return
      }
      const key = String(nodeId)
      const positions = { ...this.positions, [key]: { position, duration, updatedAt: Date.now() } }
      const keys = Object.keys(positions)
      if (keys.length > MAX_POSITIONS) {
        keys
          .sort((a, b) => (positions[a]?.updatedAt ?? 0) - (positions[b]?.updatedAt ?? 0))
          .slice(0, keys.length - MAX_POSITIONS)
          .forEach((old) => delete positions[old])
      }
      this.positions = positions
      this.persist()
    },
    positionFor(nodeId: number): SavedPosition | null {
      return this.positions[String(nodeId)] ?? null
    },
    clearPosition(nodeId: number): void {
      const key = String(nodeId)
      if (!(key in this.positions)) return
      const positions = { ...this.positions }
      delete positions[key]
      this.positions = positions
      this.persist()
    },
  },
})
