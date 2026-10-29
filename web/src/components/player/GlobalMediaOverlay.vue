<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { downloadUrl } from '../../api/client'
import { fileStem, formatClock, subtitleFormat } from '../../lib/media'
import { convertSubtitle } from '../../lib/subtitles'
import { useMediaStore } from '../../stores/media'
import { usePlayerStore } from '../../stores/player'
import AudioPlayer from './AudioPlayer.vue'
import VideoPlayer from './VideoPlayer.vue'
import VideoInfoBar from './VideoInfoBar.vue'
import PlayerPlaylist from './PlayerPlaylist.vue'

interface PlayerControls {
  toggle: () => void
  play: () => void
  pause: () => void
  seekTo: (seconds: number) => void
  seekBy: (delta: number) => void
  setVolume: (volume: number) => void
  toggleMute: () => void
  setSpeed: (speed: number) => void
  toggleFullscreen?: () => void
}

const store = useMediaStore()
const player = usePlayerStore()

const audioRef = ref<PlayerControls | null>(null)
const videoRef = ref<PlayerControls | null>(null)
const subtitleUrl = ref<string | null>(null)
const subtitleNote = ref<string | null>(null)
const subtitleLoading = ref(false)
const resumeAt = ref(0)
const playing = ref(false)
const mediaError = ref('')
const playlistOpen = ref(true)

const kind = computed(() => store.kind)
const current = computed(() => store.node)
const subtitleLabel = computed(() => {
  const format = store.subtitle ? subtitleFormat(store.subtitle.name) : null
  return format ? format.toUpperCase() : null
})
const playModeLabel = computed(() =>
  player.mode === 'one' ? '单曲循环' : player.mode === 'shuffle' ? '随机播放' : '列表循环',
)

function controls(): PlayerControls | null {
  return kind.value === 'audio' ? audioRef.value : videoRef.value
}

function close(): void {
  // A video cannot keep playing behind the page, but keep a paused mini entry
  // so the user can jump straight back into it (it restarts from 0).
  store.hide()
}

function reopen(): void {
  store.open = true
  if (kind.value === 'video') {
    // Autoplay is allowed here: the click that reopened the overlay is a gesture.
    void nextTick(() => videoRef.value?.play())
  }
}

function download(): void {
  if (!current.value) return
  const anchor = document.createElement('a')
  anchor.href = downloadUrl(current.value.id)
  anchor.download = current.value.name
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
}

function playNext(): void {
  const items = store.playlist
  if (items.length === 0) return
  const index = items.findIndex((item) => item.id === current.value?.id)
  let target = items[(index + 1) % items.length]
  if (kind.value === 'audio' && player.mode === 'shuffle') {
    const others = items.filter((item) => item.id !== current.value?.id)
    target = others[Math.floor(Math.random() * others.length)] ?? target
  }
  if (target) store.navigate(target.id)
}

function playPrev(): void {
  const items = store.playlist
  if (items.length === 0) return
  const index = items.findIndex((item) => item.id === current.value?.id)
  let target = items[(index - 1 + items.length) % items.length]
  if (kind.value === 'audio' && player.mode === 'shuffle') {
    const others = items.filter((item) => item.id !== current.value?.id)
    target = others[Math.floor(Math.random() * others.length)] ?? target
  }
  if (target) store.navigate(target.id)
}

function clearSubtitle(): void {
  if (subtitleUrl.value?.startsWith('blob:')) URL.revokeObjectURL(subtitleUrl.value)
  subtitleUrl.value = null
  subtitleNote.value = null
}

async function loadSubtitle(): Promise<void> {
  clearSubtitle()
  const sidecar = store.subtitle
  if (!sidecar) return
  const format = subtitleFormat(sidecar.name)
  if (!format) return
  if (sidecar.size > 1024 * 1024) {
    subtitleNote.value = '字幕文件超过 1 MB，已跳过'
    return
  }
  if (format === 'vtt') {
    subtitleUrl.value = downloadUrl(sidecar.id)
    return
  }
  subtitleLoading.value = true
  try {
    const response = await fetch(downloadUrl(sidecar.id))
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    const converted = convertSubtitle(sidecar.name, await response.text())
    subtitleUrl.value = URL.createObjectURL(new Blob([converted.vtt], { type: 'text/vtt' }))
    subtitleNote.value = converted.note
  } catch {
    subtitleNote.value = '字幕加载失败（可下载后使用本地播放器）'
  } finally {
    subtitleLoading.value = false
  }
}

function toggleSubtitles(): void {
  player.setShowSubtitles(!player.showSubtitles)
  if (store.subtitle && subtitleUrl.value === null) void loadSubtitle()
}

function resumeFromStart(): void {
  controls()?.seekTo(0)
  if (current.value) player.clearPosition(current.value.id)
  resumeAt.value = 0
}

function setupMediaSession(): void {
  if (!('mediaSession' in navigator) || !current.value) return
  try {
    navigator.mediaSession.metadata = new MediaMetadata({
      title: fileStem(current.value.name),
      artist: 'Rapig',
      artwork: store.cover ? [{ src: downloadUrl(store.cover.id), sizes: '512x512' }] : [],
    })
    navigator.mediaSession.setActionHandler('play', () => controls()?.play())
    navigator.mediaSession.setActionHandler('pause', () => controls()?.pause())
    navigator.mediaSession.setActionHandler('previoustrack', playPrev)
    navigator.mediaSession.setActionHandler('nexttrack', playNext)
    navigator.mediaSession.setActionHandler('seekbackward', () => controls()?.seekBy(-10))
    navigator.mediaSession.setActionHandler('seekforward', () => controls()?.seekBy(10))
  } catch {
    // Best effort only.
  }
}

watch(
  () => current.value?.id,
  () => {
    clearSubtitle()
    mediaError.value = ''
    resumeAt.value = current.value ? (player.positionFor(current.value.id)?.position ?? 0) : 0
    setupMediaSession()
    if (kind.value === 'video') void loadSubtitle()
  },
  { immediate: true },
)

onBeforeUnmount(clearSubtitle)
</script>

<template>
  <div v-if="current" class="global-media">
    <Teleport to="body">
      <Transition name="preview">
        <div v-show="store.open" class="overlay" @click.self="close">
          <section class="shell" :class="[kind, { 'no-side': !playlistOpen }]" role="dialog" aria-modal="true" aria-label="媒体播放器">
            <header class="bar">
              <div class="bar-main">
                <p class="bar-name" :title="current.name">{{ current.name }}</p>
                <p class="bar-meta">
                  {{ kind === 'audio' ? '音频' : '视频' }}
                  <span v-if="kind === 'audio'">· {{ playModeLabel }}</span>
                </p>
              </div>
              <div class="bar-actions">
                <button
                  class="icon-button"
                  type="button"
                  :aria-pressed="playlistOpen"
                  aria-label="播放列表"
                  title="播放列表"
                  @click="playlistOpen = !playlistOpen"
                >
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M3 4h10M3 8h10M3 12h6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
                  </svg>
                </button>
                <button class="icon-button" type="button" aria-label="下载" title="下载" @click="download">
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M8 2.6v7.2M5 7l3 3 3-3M3.4 12.4h9.2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                </button>
                <button class="icon-button" type="button" aria-label="收起播放器" title="收起（音频继续播放）" @click="close">
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M4 8h8" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
                  </svg>
                </button>
                <button class="icon-button close" type="button" aria-label="关闭并停止" title="关闭并停止" @click="store.stop()">
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="m4 4 8 8M12 4l-8 8" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
                  </svg>
                </button>
              </div>
            </header>

            <div class="body" :class="{ 'with-side': playlistOpen }">
              <div class="main">
                <div v-if="resumeAt > 10" class="resume-banner">
                  <span>上次播放到 {{ formatClock(resumeAt) }}，已为你继续。</span>
                  <button type="button" class="resume-link" @click="resumeFromStart">从头播放</button>
                </div>

                <!-- Audio stays mounted while the overlay is hidden so playback survives navigation. -->
                <AudioPlayer
                  v-if="kind === 'audio'"
                  ref="audioRef"
                  :node="current"
                  :cover="store.cover"
                  :lyrics="store.lyrics"
                  @next="playNext"
                  @prev="playPrev"
                  @playing="playing = $event"
                />

                <template v-else>
                  <VideoPlayer
                    v-if="store.open"
                    ref="videoRef"
                    :node="current"
                    :subtitle-src="subtitleUrl"
                    :subtitle-label="subtitleLabel"
                    :subtitles-on="player.showSubtitles"
                    @toggle-subtitles="toggleSubtitles"
                    @ended="playNext"
                    @error="mediaError = $event"
                  />
                  <p v-if="subtitleLoading" class="note">正在加载字幕…</p>
                  <p v-else-if="subtitleNote" class="note">{{ subtitleNote }}</p>
                  <p v-if="mediaError" class="note error">{{ mediaError }}</p>
                  <VideoInfoBar :node="current" :parent-id="current.parent_id" @download="download" />
                </template>
              </div>

              <aside v-show="playlistOpen" class="side">
                <PlayerPlaylist
                  :items="store.playlist"
                  :current-id="current.id"
                  :kind="kind === 'video' ? 'video' : 'audio'"
                  @select="store.navigate($event)"
                />
              </aside>
            </div>
          </section>
        </div>
      </Transition>
    </Teleport>

    <!-- Mini bar: audio keeps playing after the overlay is hidden. -->
    <div v-if="!store.open" class="mini" role="status">
      <span class="mini-mark" aria-hidden="true">{{ kind === 'audio' ? '♪' : '▶' }}</span>
      <button class="mini-title" type="button" :title="current.name" @click="reopen">
        {{ fileStem(current.name) }}
      </button>
      <button v-if="kind === 'audio'" class="mini-button" type="button" aria-label="播放/暂停" @click="audioRef?.toggle()">
        <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M5 3.4v9.2l7.4-4.6z" fill="currentColor" />
        </svg>
      </button>
      <button v-if="kind === 'audio'" class="mini-button" type="button" aria-label="下一首" @click="playNext">
        <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M4 3.6v8.8l6-4.4zM11.4 3.6v8.8" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
      <button class="mini-button" type="button" aria-label="展开播放器" @click="reopen">
        <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M6 3.5 10.5 8 6 12.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
      <button class="mini-button danger" type="button" aria-label="停止播放" @click="store.stop()">
        <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
          <path d="m4 4 8 8M12 4l-8 8" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
        </svg>
      </button>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  z-index: 90;
  display: grid;
  place-items: center;
  padding: 24px;
  background: rgba(12, 10, 9, 0.74);
  backdrop-filter: blur(3px);
}

.shell {
  display: flex;
  width: min(1120px, 100%);
  max-height: min(88vh, 100%);
  flex-direction: column;
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.shell.audio {
  width: min(1000px, 100%);
}

/* 收起播放列表时收窄外壳，让主区宽度与展开时一致（避免比例被拉长）。 */
.shell.audio.no-side {
  width: min(748px, 100%);
}

.shell.no-side .body:not(.with-side) {
  grid-template-columns: minmax(0, 1fr);
}

.shell.video.no-side {
  width: min(844px, 100%);
}

.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 12px 10px 16px;
  border-bottom: 1px solid var(--color-border);
}

.bar-main {
  min-width: 0;
}

.bar-name {
  overflow: hidden;
  font-size: var(--fs-base);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bar-meta {
  margin-top: 1px;
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.bar-actions {
  display: flex;
  flex-shrink: 0;
  align-items: center;
  gap: 6px;
}

.icon-button {
  display: grid;
  width: 32px;
  height: 32px;
  place-items: center;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-soft);
  cursor: pointer;
}

.icon-button:hover {
  background: var(--color-surface-soft);
  color: var(--color-text);
}

.icon-button.close:hover {
  background: rgba(249, 115, 22, 0.12);
  color: var(--color-primary-ink);
}

.body {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 16px;
  min-height: 0;
  padding: 14px;
  overflow: auto;
}

.body.with-side {
  grid-template-columns: minmax(0, 1fr) 264px;
}

.shell.audio .body.with-side {
  grid-template-columns: minmax(0, 1fr) 240px;
}

.main {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 10px;
}

.side {
  min-width: 0;
}

.resume-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 10px;
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-size: 13px;
}

.resume-link {
  border: 0;
  background: transparent;
  color: var(--color-primary-ink);
  cursor: pointer;
  font-size: 13px;
  text-decoration: underline;
}

.note {
  margin: 0;
  color: var(--color-muted);
  font-size: 12px;
}

.note.error {
  color: var(--color-danger, #b42318);
}

.mini {
  position: fixed;
  right: 18px;
  bottom: 18px;
  z-index: 80;
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: min(520px, calc(100vw - 36px));
  padding: 8px 10px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 14px;
  box-shadow: var(--shadow-pop);
}

.mini-mark {
  display: grid;
  width: 26px;
  height: 24px;
  place-items: center;
  border-radius: 8px;
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-size: 13px;
}

.mini-title {
  overflow: hidden;
  max-width: 220px;
  border: 0;
  background: transparent;
  color: var(--color-text);
  cursor: pointer;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mini-button {
  display: grid;
  width: 28px;
  height: 26px;
  place-items: center;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-soft);
  cursor: pointer;
}

.mini-button:hover {
  background: var(--color-surface-soft);
  color: var(--color-text);
}

.mini-button.danger:hover {
  color: var(--color-primary-ink);
}

@media (max-width: 760px) {
  .body,
  .shell.audio .body {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
