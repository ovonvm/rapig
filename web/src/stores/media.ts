import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import type { NodeItem } from '../api/client'
import { findSidecar, mediaKind, mediaSiblings, type MediaKind } from '../lib/media'

/**
 * Global media player state.
 *
 * The overlay is mounted once by AppShell, so audio keeps playing while the
 * user navigates the rest of the site (the lesson from Rapig: a page-local
 * overlay died on every route change). Everything is cleared on logout and on
 * account switch so one user can never hear another user's track.
 */
export const useMediaStore = defineStore('media', () => {
  const open = ref(false)
  const node = ref<NodeItem | null>(null)
  /** All files of the directory the current track came from (playlist + sidecars). */
  const dirFiles = ref<NodeItem[]>([])

  const kind = computed<MediaKind | null>(() => (node.value ? mediaKind(node.value.name) : null))
  const playlist = computed(() => {
    if (!node.value || !kind.value) return []
    const list = mediaSiblings(dirFiles.value, kind.value)
    if (!list.some((item) => item.id === node.value?.id)) list.unshift(node.value)
    return list
  })
  const cover = computed(() => (node.value && kind.value === 'audio' ? findSidecar(dirFiles.value, node.value, 'cover') : null))
  const lyrics = computed(() => (node.value && kind.value === 'audio' ? findSidecar(dirFiles.value, node.value, 'lyrics') : null))
  const subtitle = computed(() => (node.value && kind.value === 'video' ? findSidecar(dirFiles.value, node.value, 'subtitles') : null))

  function openWith(target: NodeItem, files: NodeItem[]): void {
    if (!mediaKind(target.name)) return
    node.value = target
    dirFiles.value = files
    open.value = true
  }

  /** Hide the overlay; audio keeps playing (use stop() to end playback). */
  function hide(): void {
    open.value = false
  }

  /** Navigate inside the current queue without unloading the audio element. */
  function navigate(target: NodeItem | number): void {
    const item = typeof target === 'number' ? playlist.value.find((entry) => entry.id === target) : target
    if (!item) return
    node.value = item
    open.value = true
  }

  /** Fully stop: clear state so the next user cannot inherit a track. */
  function stop(): void {
    open.value = false
    node.value = null
    dirFiles.value = []
  }

  return { open, node, dirFiles, kind, playlist, cover, lyrics, subtitle, openWith, hide, navigate, stop }
})
