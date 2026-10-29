<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { NodeItem } from '../../api/client'
import { downloadUrl } from '../../api/client'
import { formatBytes } from '../../lib/format'
import { extensionOf, fileStem, formatClock } from '../../lib/media'
import { activeLyricIndex, parseLrc, type LyricLine } from '../../lib/lyrics'
import { usePlayerStore, type PlayMode } from '../../stores/player'

const props = defineProps<{
  node: NodeItem
  cover: NodeItem | null
  lyrics: NodeItem | null
}>()
const emit = defineEmits<{ next: []; prev: []; playing: [boolean] }>()

const store = usePlayerStore()
const audioRef = ref<HTMLAudioElement | null>(null)
const canvasRef = ref<HTMLCanvasElement | null>(null)
const lyricsBoxRef = ref<HTMLElement | null>(null)

const playing = ref(false)
const autoplayPending = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const bufferedEnd = ref(0)
const errorText = ref('')
const coverBroken = ref(false)
const lyricLines = ref<LyricLine[]>([])
const lyricsFetching = ref(false)

let audioCtx: AudioContext | null = null
let analyser: AnalyserNode | null = null
let rafId = 0
let lastSaved = 0

const src = computed(() => downloadUrl(props.node.id))
const coverSrc = computed(() => (props.cover && !coverBroken.value ? downloadUrl(props.cover.id) : null))
const title = computed(() => fileStem(props.node.name))
const formatLabel = computed(() => extensionOf(props.node.name).toUpperCase() || '音频')
const activeLyric = computed(() => activeLyricIndex(lyricLines.value, currentTime.value * 1000))
const progressPercent = computed(() => (duration.value > 0 ? Math.min(100, (currentTime.value / duration.value) * 100) : 0))

const modeLabel = computed(() => (store.mode === 'list' ? '列表循环' : store.mode === 'one' ? '单曲循环' : '随机播放'))
const modeIcon = computed(() => {
  if (store.mode === 'one') return '1'
  if (store.mode === 'shuffle') return '⇄'
  return '↻'
})

function audioEl(): HTMLAudioElement | null {
  return audioRef.value
}

function applyVolume(): void {
  const audio = audioEl()
  if (!audio) return
  audio.volume = store.volume
  audio.muted = store.muted
}

function setupSpectrum(): void {
  const audio = audioEl()
  const canvas = canvasRef.value
  if (!audio || !canvas || audioCtx) return
  const Ctx = window.AudioContext
  if (!Ctx) return
  audioCtx = new Ctx()
  try {
    const source = audioCtx.createMediaElementSource(audio)
    analyser = audioCtx.createAnalyser()
    analyser.fftSize = 512
    source.connect(analyser)
    analyser.connect(audioCtx.destination)
  } catch {
    // Media element sources can only be created once; playback still works.
    audioCtx = null
    analyser = null
  }
  drawSpectrum()
}

function drawSpectrum(): void {
  cancelAnimationFrame(rafId)
  const canvas = canvasRef.value
  if (!canvas) return
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  const ratio = window.devicePixelRatio || 1
  const width = canvas.clientWidth
  const height = canvas.clientHeight
  if (canvas.width !== Math.round(width * ratio) || canvas.height !== Math.round(height * ratio)) {
    canvas.width = Math.max(1, Math.round(width * ratio))
    canvas.height = Math.max(1, Math.round(height * ratio))
  }
  ctx.setTransform(ratio, 0, 0, ratio, 0, 0)
  const bars = 42
  const gap = 3
  const barWidth = Math.max(3, (width - gap * (bars - 1)) / bars)
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  const buffer = new Uint8Array(analyser?.frequencyBinCount ?? 128)

  const draw = (): void => {
    ctx.clearRect(0, 0, width, height)
    if (!playing.value || reduceMotion) {
      // Idle: one calm baseline instead of fake bars.
      ctx.fillStyle = 'rgba(249, 115, 22, 0.28)'
      ctx.beginPath()
      ctx.roundRect(0, height - 3, width, 3, 2)
      ctx.fill()
      return
    }
    if (analyser) analyser.getByteFrequencyData(buffer)
    const gradient = ctx.createLinearGradient(0, height, 0, 0)
    gradient.addColorStop(0, 'rgba(249, 115, 22, 0.35)')
    gradient.addColorStop(1, 'rgba(249, 115, 22, 0.9)')
    ctx.fillStyle = gradient
    for (let i = 0; i < bars; i += 1) {
      const value = analyser && playing.value && !reduceMotion ? buffer[i * 2] ?? 0 : 18
      const barHeight = Math.max(4, (value / 255) * height * (playing.value ? 1 : 0.3))
      const x = i * (barWidth + gap)
      const y = height - barHeight
      const radius = Math.min(barWidth / 2, 3)
      ctx.beginPath()
      ctx.roundRect(x, y, barWidth, barHeight, radius)
      ctx.fill()
    }
    rafId = requestAnimationFrame(draw)
  }
  draw()
}

async function play(fromAutoplay = false): Promise<void> {
  const audio = audioEl()
  if (!audio || errorText.value) return
  if (!audioCtx) setupSpectrum()
  if (audioCtx?.state === 'suspended') await audioCtx.resume().catch(() => undefined)
  try {
    await audio.play()
    if (analyser) drawSpectrum()
  } catch (error: unknown) {
    if (fromAutoplay && error instanceof DOMException && error.name === 'NotAllowedError') {
      // 浏览器自动播放策略：先用静音播放取得播放权，再恢复音量。
      try {
        audio.muted = true
        await audio.play()
        audio.muted = false
        audio.volume = store.volume
        return
      } catch {
        return
      }
    }
    if (fromAutoplay) return
    if (error instanceof DOMException && error.name === 'NotAllowedError') {
      errorText.value = '浏览器阻止了播放，请再次点击播放按钮'
    } else {
      errorText.value = '无法播放该音频，请下载后使用本地播放器'
    }
  }
}

function pause(): void {
  audioEl()?.pause()
}

function toggle(): void {
  if (playing.value) pause()
  else void play()
}

function seekTo(seconds: number): void {
  const audio = audioEl()
  if (!audio || !Number.isFinite(seconds)) return
  audio.currentTime = Math.min(Math.max(0, seconds), duration.value || seconds)
}

function seekBy(delta: number): void {
  seekTo(currentTime.value + delta)
}

function setVolume(volume: number): void {
  store.setVolume(volume)
  applyVolume()
}

function toggleMute(): void {
  store.setMuted(!store.muted)
  applyVolume()
}

function setSpeed(speed: number): void {
  store.setSpeed(speed)
  const audio = audioEl()
  if (audio) audio.playbackRate = speed
}

function cycleMode(): void {
  const order: PlayMode[] = ['list', 'one', 'shuffle']
  const next = order[(order.indexOf(store.mode) + 1) % order.length] ?? 'list'
  store.setMode(next)
}

function onTimeUpdate(): void {
  const audio = audioEl()
  if (!audio) return
  currentTime.value = audio.currentTime
  if (audio.buffered.length > 0) bufferedEnd.value = audio.buffered.end(audio.buffered.length - 1)
  const now = Date.now()
  if (playing.value && now - lastSaved > 5000) {
    lastSaved = now
    store.rememberPosition(props.node.id, audio.currentTime, audio.duration || duration.value)
  }
  const index = activeLyric.value
  if (index >= 0 && lyricsBoxRef.value) {
    const row = lyricsBoxRef.value.querySelectorAll<HTMLElement>('[data-lyric]')[index]
    row?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }
}

function onLoadedMetadata(): void {
  const audio = audioEl()
  if (!audio) return
  duration.value = Number.isFinite(audio.duration) ? audio.duration : 0
  audio.playbackRate = store.speed
  const saved = store.positionFor(props.node.id)
  if (saved && saved.position > 10 && duration.value > saved.position + 5) {
    audio.currentTime = saved.position
  }
  if (autoplayPending.value) {
    autoplayPending.value = false
    void play(true)
  }
}

function onEnded(): void {
  const audio = audioEl()
  if (audio && store.mode === 'one') {
    audio.currentTime = 0
    void play()
    return
  }
  if (audio) store.clearPosition(props.node.id)
  emit('next')
}

function onPlay(): void {
  playing.value = true
  emit('playing', true)
}

function onPause(): void {
  playing.value = false
  emit('playing', false)
}

function onError(): void {
  errorText.value = '浏览器不支持该音频格式，或文件已损坏。'
}

function onProgress(): void {
  const audio = audioEl()
  if (!audio || audio.buffered.length === 0) return
  bufferedEnd.value = audio.buffered.end(audio.buffered.length - 1)
}

function onSeekInput(event: Event): void {
  seekTo(Number((event.target as HTMLInputElement).value))
}

function onVolumeInput(event: Event): void {
  setVolume(Number((event.target as HTMLInputElement).value) / 100)
}

function onSpeedChange(event: Event): void {
  setSpeed(Number((event.target as HTMLSelectElement).value))
}

async function loadLyrics(): Promise<void> {
  const sidecar = props.lyrics
  lyricLines.value = []
  if (!sidecar || sidecar.size > 512 * 1024) return
  lyricsFetching.value = true
  try {
    const response = await fetch(downloadUrl(sidecar.id))
    if (!response.ok) return
    lyricLines.value = parseLrc(await response.text())
  } finally {
    lyricsFetching.value = false
  }
}

watch(
  () => props.lyrics,
  () => {
    void loadLyrics()
  },
  { immediate: true },
)

watch(
  () => props.node.id,
  () => {
    currentTime.value = 0
    duration.value = 0
    bufferedEnd.value = 0
    errorText.value = ''
    coverBroken.value = false
    playing.value = false
    autoplayPending.value = true
  },
)

watch(
  () => [store.volume, store.muted],
  () => applyVolume(),
)

function onResize(): void {
  if (!playing.value) drawSpectrum()
}

onMounted(() => {
  applyVolume()
  const audio = audioEl()
  if (audio) audio.volume = store.volume
  drawSpectrum()
  autoplayPending.value = true
  // 若 metadata 已缓存（同一会话再打开），loadedmetadata 不会重复触发，这里补一次。
  if (audio) void nextTick(() => play(true))
  window.addEventListener('resize', onResize)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  cancelAnimationFrame(rafId)
  const audio = audioEl()
  if (audio && duration.value > 0) store.rememberPosition(props.node.id, audio.currentTime, duration.value)
  void audioCtx?.close().catch(() => undefined)
  audioCtx = null
  analyser = null
})

defineExpose({ toggle, play, pause, seekTo, seekBy, setVolume, toggleMute, cycleMode, setSpeed, getDuration: () => duration.value })
</script>

<template>
  <section class="audio-card" aria-label="音频播放器">
    <div class="audio-hero">
      <div class="cover" :class="{ placeholder: !coverSrc }">
        <img v-if="coverSrc" :src="coverSrc" alt="" @error="coverBroken = true" />
        <span v-else class="cover-mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" width="46" height="46">
            <path d="M9 18.5A2.5 2.5 0 1 0 6.5 16c.35 0 .68.07 1 .2V8l10-2v8.5a2.5 2.5 0 1 0 2.5-2.5c-.35 0-.68.07-1 .2V3L7 5.2v10.3" fill="currentColor" opacity="0.55" />
          </svg>
        </span>
      </div>

      <div class="audio-meta">
        <p class="eyebrow">正在播放</p>
        <h1 class="track-title">{{ title }}</h1>
        <div class="chips">
          <span>{{ formatLabel }}</span>
          <span>{{ formatBytes(node.size) }}</span>
          <span>{{ modeLabel }}</span>
        </div>

        <canvas ref="canvasRef" class="spectrum" :class="{ idle: !playing }" aria-hidden="true" />

        <div class="progress">
          <input
            class="seek"
            type="range"
            min="0"
            :max="duration || 0"
            step="0.1"
            :value="currentTime"
            aria-label="播放进度"
            :style="{ '--filled': progressPercent + '%' }"
            @input="onSeekInput"
          />
          <div class="times">
            <span>{{ formatClock(currentTime) }}</span>
            <span>{{ formatClock(duration) }}</span>
          </div>
        </div>

        <div class="controls">
          <button type="button" class="mode" :aria-label="modeLabel" @click="cycleMode">{{ modeIcon }}</button>
          <button type="button" class="icon-btn" aria-label="上一首" @click="emit('prev')">
            <svg viewBox="0 0 24 24" width="20" height="20"><path d="M6 6h2v12H6zM20 6v12l-9-6z" fill="currentColor" /></svg>
          </button>
          <button type="button" class="play" :aria-label="playing ? '暂停' : '播放'" @click="toggle">
            <svg v-if="playing" viewBox="0 0 24 24" width="26" height="26">
              <rect x="6" y="5" width="4" height="14" rx="1" fill="currentColor" />
              <rect x="14" y="5" width="4" height="14" rx="1" fill="currentColor" />
            </svg>
            <svg v-else viewBox="0 0 24 24" width="26" height="26"><path d="M8 5v14l11-7z" fill="currentColor" /></svg>
          </button>
          <button type="button" class="icon-btn" aria-label="下一首" @click="emit('next')">
            <svg viewBox="0 0 24 24" width="20" height="20"><path d="M16 6h2v12h-2zM4 6v12l9-6z" fill="currentColor" /></svg>
          </button>

          <span class="spacer" />

          <button type="button" class="icon-btn" aria-label="静音" @click="toggleMute">
            <svg viewBox="0 0 24 24" width="18" height="18">
              <path d="M4 9v6h4l5 4V5L8 9H4z" fill="currentColor" />
              <path v-if="!store.muted && store.volume > 0" d="M16 8.5a4.5 4.5 0 0 1 0 7" stroke="currentColor" stroke-width="1.6" fill="none" stroke-linecap="round" />
            </svg>
          </button>
          <input
            class="volume"
            type="range"
            min="0"
            max="100"
            :value="Math.round(store.volume * 100)"
            aria-label="音量"
            @input="onVolumeInput"
          />
          <select class="speed" :value="String(store.speed)" aria-label="播放速度" @change="onSpeedChange">
            <option value="0.5">0.5x</option>
            <option value="0.75">0.75x</option>
            <option value="1">1x</option>
            <option value="1.25">1.25x</option>
            <option value="1.5">1.5x</option>
            <option value="2">2x</option>
          </select>
        </div>
      </div>
    </div>

    <div v-if="lyricLines.length" class="lyrics" aria-label="歌词">
      <header class="lyrics-head">歌词</header>
      <div ref="lyricsBox" class="lyrics-box">
        <button
          v-for="(line, index) in lyricLines"
          :key="index"
          type="button"
          class="lyric-line"
          :class="{ active: index === activeLyric }"
          data-lyric
          @click="seekTo(line.time / 1000)"
        >
          {{ line.text || '♪' }}
        </button>
      </div>
    </div>

    <p v-if="errorText" class="media-error">{{ errorText }}</p>

    <audio
      ref="audioRef"
      :src="src"
      preload="metadata"
      class="sr-only-audio"
      @play="onPlay"
      @pause="onPause"
      @timeupdate="onTimeUpdate"
      @loadedmetadata="onLoadedMetadata"
      @progress="onProgress"
      @ended="onEnded"
      @error="onError"
    />
  </section>
</template>

<style scoped>
.audio-card {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 22px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface);
  min-width: 0;
}

.audio-hero {
  display: flex;
  gap: 22px;
}

.cover {
  position: relative;
  flex: none;
  width: 190px;
  aspect-ratio: 1;
  border-radius: 12px;
  overflow: hidden;
  background: linear-gradient(140deg, #fdba74, #fb923c 55%, #ea580c);
}

.cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.cover-mark {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  color: rgba(28, 25, 23, 0.72);
}

.audio-meta {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.eyebrow {
  margin: 0;
  font-size: 12px;
  color: var(--color-primary-ink);
  letter-spacing: 0.12em;
}

.track-title {
  margin: 6px 0 8px;
  font-size: 24px;
  line-height: 1.2;
  color: var(--color-text);
  word-break: break-word;
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.chips span {
  padding: 2px 9px;
  border-radius: 999px;
  font-size: 12px;
  color: var(--color-primary-ink);
  background: var(--color-primary-soft);
}

.spectrum {
  width: 100%;
  height: 64px;
  margin: 14px 0 4px;
}

.spectrum.idle {
  opacity: 0.55;
}

.progress {
  margin-top: auto;
}

.seek {
  width: 100%;
  appearance: none;
  height: 4px;
  border-radius: 999px;
  background: linear-gradient(
    to right,
    #f97316 0%,
    #f97316 var(--filled, 0%),
    var(--color-border) var(--filled, 0%),
    var(--color-border) 100%
  );
  cursor: pointer;
}

.seek::-webkit-slider-thumb {
  appearance: none;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: #fff;
  border: 2px solid #f97316;
}

.times {
  display: flex;
  justify-content: space-between;
  margin-top: 4px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--color-muted);
}

.controls {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-top: 10px;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text);
  cursor: pointer;
}

.icon-btn:hover {
  background: var(--color-primary-soft);
}

.mode {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}

.play {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 40px;
  border: 0;
  border-radius: 10px;
  background: var(--color-primary);
  color: var(--color-on-primary);
  cursor: pointer;
}

.play:hover {
  background: var(--color-primary-hover);
}

.spacer {
  flex: 1;
}

.volume {
  width: 130px;
  accent-color: var(--color-primary);
}

.speed {
  padding: 4px 6px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-surface);
  color: var(--color-text);
  font-size: 12px;
}

.lyrics {
  border-top: 1px solid var(--color-border);
  padding-top: 10px;
}

.lyrics-head {
  font-size: 13px;
  color: var(--color-muted);
  margin-bottom: 6px;
}

.lyrics-box {
  max-height: 168px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.lyric-line {
  padding: 5px 8px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--color-muted);
  font-size: 14px;
  text-align: left;
  cursor: pointer;
}

.lyric-line.active {
  color: var(--color-primary-ink);
  background: var(--color-primary-soft);
  font-weight: 600;
}

.media-error {
  margin: 0;
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--color-danger-soft);
  color: var(--color-danger);
  font-size: 13px;
}

.sr-only-audio {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
}

@media (max-width: 900px) {
  .audio-hero {
    flex-direction: column;
  }

  .cover {
    width: 100%;
    max-width: 220px;
    margin: 0 auto;
  }

  .volume,
  .speed {
    display: none;
  }
}
</style>
