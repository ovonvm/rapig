<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { NodeItem } from '../../api/client'
import { downloadUrl } from '../../api/client'
import { formatClock } from '../../lib/media'
import { usePlayerStore } from '../../stores/player'

const props = defineProps<{
  node: NodeItem
  subtitleSrc: string | null
  subtitleLabel: string | null
  subtitlesOn: boolean
}>()
const emit = defineEmits<{
  'toggle-subtitles': []
  ended: []
  error: [message: string]
  loaded: [duration: number]
}>()

const store = usePlayerStore()
const videoRef = ref<HTMLVideoElement | null>(null)
const stageRef = ref<HTMLElement | null>(null)

const playing = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const bufferedEnd = ref(0)
const errorText = ref('')
const controlsVisible = ref(true)
let hideTimer: number | null = null
let lastSaved = 0

const src = computed(() => downloadUrl(props.node.id))
const progressPercent = computed(() => (duration.value > 0 ? Math.min(100, (currentTime.value / duration.value) * 100) : 0))
const bufferedPercent = computed(() => (duration.value > 0 ? Math.min(100, (bufferedEnd.value / duration.value) * 100) : 0))

function videoEl(): HTMLVideoElement | null {
  return videoRef.value
}

function applyVolume(): void {
  const video = videoEl()
  if (!video) return
  video.volume = store.volume
  video.muted = store.muted
}

function revokeError(): void {
  errorText.value = ''
}

function play(): void {
  const video = videoEl()
  if (!video || errorText.value) return
  revokeError()
  void video
    .play()
    .then(() => {
      keyboardFocus()
    })
    .catch((error: unknown) => {
      // Autoplay restrictions are the only recoverable failure; real codec
      // problems arrive through the `error` event instead.
      if (error instanceof DOMException && error.name === 'NotAllowedError') {
        errorText.value = '浏览器阻止了自动播放，请点击播放按钮'
        return
      }
      errorText.value = '无法播放该视频，请下载后使用本地播放器'
    })
}

function pause(): void {
  videoEl()?.pause()
}

function toggle(): void {
  if (playing.value) pause()
  else play()
}

function seekTo(seconds: number): void {
  const video = videoEl()
  if (!video || !Number.isFinite(seconds)) return
  video.currentTime = Math.min(Math.max(0, seconds), duration.value || seconds)
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
  const video = videoEl()
  if (video) video.playbackRate = speed
}

function keyboardFocus(): void {
  stageRef.value?.focus()
}

async function toggleFullscreen(): Promise<void> {
  const stage = stageRef.value
  if (!stage) return
  if (document.fullscreenElement) {
    await document.exitFullscreen().catch(() => undefined)
    return
  }
  await stage.requestFullscreen().catch(() => undefined)
}

function onTimeUpdate(): void {
  const video = videoEl()
  if (!video) return
  currentTime.value = video.currentTime
  if (video.buffered.length > 0) {
    bufferedEnd.value = video.buffered.end(video.buffered.length - 1)
  }
  const now = Date.now()
  if (now - lastSaved > 5000 && playing.value) {
    lastSaved = now
    store.rememberPosition(props.node.id, video.currentTime, video.duration || duration.value)
  }
}

function onLoadedMetadata(): void {
  const video = videoEl()
  if (!video) return
  duration.value = Number.isFinite(video.duration) ? video.duration : 0
  video.playbackRate = store.speed
  lastSaved = 0
  emit('loaded', duration.value)
}

function onEnded(): void {
  const video = videoEl()
  if (video) store.clearPosition(props.node.id)
  emit('ended')
}

function onError(): void {
  errorText.value = '浏览器不支持该视频格式，或文件已损坏。'
  emit('error', errorText.value)
}

function onProgress(): void {
  const video = videoEl()
  if (!video || video.buffered.length === 0) return
  bufferedEnd.value = video.buffered.end(video.buffered.length - 1)
}

function onSeekInput(event: Event): void {
  const input = event.target as HTMLInputElement
  seekTo(Number(input.value))
}

function onVolumeInput(event: Event): void {
  const input = event.target as HTMLInputElement
  setVolume(Number(input.value) / 100)
}

function onSpeedChange(event: Event): void {
  const select = event.target as HTMLSelectElement
  setSpeed(Number(select.value))
}

function refreshControls(): void {
  controlsVisible.value = true
  if (hideTimer !== null) window.clearTimeout(hideTimer)
  if (!playing.value) return
  hideTimer = window.setTimeout(() => {
    controlsVisible.value = false
  }, 3000)
}

function applySubtitleMode(): void {
  const video = videoEl()
  if (!video || !props.subtitleSrc) return
  const track = video.textTracks[0]
  if (track) track.mode = props.subtitlesOn ? 'showing' : 'hidden'
}

watch(
  () => props.subtitlesOn,
  async () => {
    await nextTick()
    applySubtitleMode()
  },
)

watch(
  () => props.subtitleSrc,
  async () => {
    await nextTick()
    applySubtitleMode()
  },
)

watch(
  () => props.node.id,
  () => {
    currentTime.value = 0
    duration.value = 0
    bufferedEnd.value = 0
    errorText.value = ''
    playing.value = false
    revokeError()
  },
)

watch(
  () => [store.volume, store.muted],
  () => applyVolume(),
)

onBeforeUnmount(() => {
  if (hideTimer !== null) window.clearTimeout(hideTimer)
  const video = videoEl()
  if (video && duration.value > 0) store.rememberPosition(props.node.id, video.currentTime, duration.value)
})

defineExpose({ toggle, play, pause, seekTo, seekBy, setVolume, toggleMute, setSpeed, toggleFullscreen, getDuration: () => duration.value })
</script>

<template>
  <div class="video-shell">
    <div
      ref="stageRef"
      class="video-stage"
      tabindex="-1"
      role="region"
      aria-label="视频播放器"
      @mousemove="refreshControls"
      @mouseleave="playing && refreshControls()"
    >
      <video
        ref="videoRef"
        class="video-el"
        :src="src"
        preload="metadata"
        playsinline
        @click="toggle"
        @play="playing = true; refreshControls()"
        @pause="playing = false; controlsVisible = true"
        @timeupdate="onTimeUpdate"
        @loadedmetadata="onLoadedMetadata"
        @progress="onProgress"
        @ended="onEnded"
        @error="onError"
      >
        <track
          v-if="subtitleSrc"
          :src="subtitleSrc"
          kind="subtitles"
          srclang="zh"
          :label="subtitleLabel ?? '中文字幕'"
          :default="subtitlesOn"
        />
      </video>

      <div v-if="!playing && !errorText" class="center-play">
        <button type="button" class="play-big" aria-label="播放" @click="play">
          <svg viewBox="0 0 24 24" width="34" height="34"><path d="M8 5v14l11-7z" fill="currentColor" /></svg>
        </button>
      </div>

      <div v-if="errorText" class="video-fallback">
        <p class="fallback-title">无法播放</p>
        <p class="fallback-text">{{ errorText }}</p>
        <a class="fallback-link" :href="src" download>下载文件</a>
      </div>

      <div class="controls" :class="{ hidden: !controlsVisible }" @mousemove.stop="refreshControls">
        <input
          class="seek"
          type="range"
          min="0"
          :max="duration || 0"
          step="0.1"
          :value="currentTime"
          aria-label="播放进度"
          :style="{ '--filled': progressPercent + '%', '--buffered': bufferedPercent + '%' }"
          @input="onSeekInput"
        />
        <div class="controls-row">
          <button type="button" class="icon-btn" :aria-label="playing ? '暂停' : '播放'" @click="toggle">
            <svg v-if="playing" viewBox="0 0 24 24" width="20" height="20">
              <rect x="6" y="5" width="4" height="14" rx="1" fill="currentColor" />
              <rect x="14" y="5" width="4" height="14" rx="1" fill="currentColor" />
            </svg>
            <svg v-else viewBox="0 0 24 24" width="20" height="20"><path d="M8 5v14l11-7z" fill="currentColor" /></svg>
          </button>

          <span class="time">{{ formatClock(currentTime) }} / {{ formatClock(duration) }}</span>

          <span class="spacer" />

          <button type="button" class="icon-btn" aria-label="静音" @click="toggleMute">
            <svg viewBox="0 0 24 24" width="18" height="18">
              <path
                d="M4 9v6h4l5 4V5L8 9H4z"
                fill="currentColor"
              />
              <path v-if="!store.muted && store.volume > 0" d="M16 8.5a4.5 4.5 0 0 1 0 7" stroke="currentColor" stroke-width="1.6" fill="none" stroke-linecap="round" />
              <path v-else d="M16 9l5 6M21 9l-5 6" stroke="currentColor" stroke-width="1.6" fill="none" stroke-linecap="round" />
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

          <label class="speed">
            <span class="sr-only">播放速度</span>
            <select :value="String(store.speed)" aria-label="播放速度" @change="onSpeedChange">
              <option value="0.5">0.5x</option>
              <option value="0.75">0.75x</option>
              <option value="1">1x</option>
              <option value="1.25">1.25x</option>
              <option value="1.5">1.5x</option>
              <option value="2">2x</option>
            </select>
          </label>

          <button
            v-if="subtitleSrc"
            type="button"
            class="icon-btn text-btn"
            :class="{ active: subtitlesOn }"
            :aria-pressed="subtitlesOn"
            aria-label="字幕"
            @click="emit('toggle-subtitles')"
          >
            字幕
          </button>

          <button type="button" class="icon-btn" aria-label="全屏" @click="toggleFullscreen">
            <svg viewBox="0 0 24 24" width="18" height="18">
              <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" stroke="currentColor" stroke-width="1.7" fill="none" stroke-linecap="round" />
            </svg>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.video-shell {
  width: 100%;
}

.video-stage {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #0b0b0b;
  border-radius: 14px;
  overflow: hidden;
  outline: none;
}

.video-el {
  width: 100%;
  height: 100%;
  display: block;
  background: #000;
  object-fit: contain;
}

.center-play {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
}

.play-big {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 104px;
  height: 60px;
  border: 0;
  border-radius: 12px;
  background: rgba(249, 115, 22, 0.92);
  color: #1c1917;
  cursor: pointer;
  pointer-events: auto;
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.35);
}

.play-big:hover {
  background: #ea580c;
}

.video-fallback {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 24px;
  color: #f5f5f4;
  text-align: center;
  background: rgba(12, 12, 12, 0.92);
}

.fallback-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}

.fallback-text {
  margin: 0;
  color: #d6d3d1;
  font-size: 14px;
}

.fallback-link {
  margin-top: 6px;
  padding: 8px 16px;
  border-radius: 8px;
  background: #f97316;
  color: #1c1917;
  text-decoration: none;
  font-size: 14px;
}

.controls {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  padding: 24px 14px 10px;
  background: linear-gradient(to top, rgba(0, 0, 0, 0.75), rgba(0, 0, 0, 0));
  transition: opacity 0.2s ease;
}

.controls.hidden {
  opacity: 0;
  pointer-events: none;
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
    rgba(255, 255, 255, 0.5) var(--filled, 0%),
    rgba(255, 255, 255, 0.5) var(--buffered, 0%),
    rgba(255, 255, 255, 0.25) var(--buffered, 0%),
    rgba(255, 255, 255, 0.25) 100%
  );
  cursor: pointer;
}

.seek::-webkit-slider-thumb {
  appearance: none;
  width: 13px;
  height: 13px;
  border-radius: 50%;
  background: #fff;
  border: 2px solid #f97316;
}

.controls-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
  color: #fafaf9;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 4px 6px;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  border-radius: 6px;
}

.icon-btn:hover,
.icon-btn.active {
  background: rgba(255, 255, 255, 0.14);
}

.text-btn {
  font-size: 12px;
  border: 1px solid rgba(255, 255, 255, 0.3);
}

.time {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: #e7e5e4;
}

.spacer {
  flex: 1;
}

.volume {
  width: 84px;
  accent-color: #f97316;
}

.speed select {
  padding: 2px 4px;
  border: 1px solid rgba(255, 255, 255, 0.3);
  border-radius: 6px;
  background: rgba(0, 0, 0, 0.35);
  color: inherit;
  font-size: 12px;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  border: 0;
}

:fullscreen .video-stage {
  border-radius: 0;
}

@media (max-width: 700px) {
  .volume {
    display: none;
  }
  .speed select {
    display: none;
  }
}
</style>
