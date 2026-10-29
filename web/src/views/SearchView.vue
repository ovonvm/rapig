<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { LocationQueryRaw } from 'vue-router'
import PreviewOverlay from '../components/PreviewOverlay.vue'
import { copyNode, downloadUrl } from '../api/client'
import type { NodeItem, SearchType } from '../api/client'
import { messageForError } from '../lib/errors'
import { formatBytes, formatDateTime } from '../lib/format'
import { useDownloadsStore } from '../stores/downloads'
import {
  normalizeSearchPage,
  normalizeSearchQuery,
  normalizeSearchSort,
  normalizeSearchType,
  useSearchStore,
} from '../stores/search'
import type { SearchSort } from '../stores/search'
import { useToast } from '../composables/useToast'

const route = useRoute()
const router = useRouter()
const search = useSearchStore()
const downloads = useDownloadsStore()
const toast = useToast()

/** The URL is the source of truth: refresh and shared links reproduce a search. */
const query = computed(() => normalizeSearchQuery(route.query.q))
const type = computed(() => normalizeSearchType(route.query.type))
const sort = computed(() => normalizeSearchSort(route.query.recent))
const requestedPage = computed(() => normalizeSearchPage(route.query.page))

const TYPE_FILTERS: { value: SearchType; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'file', label: '文件' },
  { value: 'dir', label: '文件夹' },
]

const SORT_FILTERS: { value: SearchSort; label: string }[] = [
  { value: 'relevance', label: '相关度' },
  { value: 'recent', label: '最近修改' },
]

const previewOpen = ref(false)
const previewId = ref<number | null>(null)
const copyingIds = ref<number[]>([])

const previewFiles = computed(() => search.items.filter((node) => node.kind === 'file'))

const title = computed(() => {
  if (!query.value) return '搜索文件'
  const base = `搜索 “${query.value}”`
  if (search.loading && !search.searched) return base
  return `${base} · ${search.total} 项`
})

const summary = computed(() => {
  if (!query.value || search.loading || search.error) return ''
  if (search.hasMore) return `已显示 ${search.items.length} 项，共 ${search.total} 项`
  return search.total > 0 ? `共 ${search.total} 项` : ''
})

function sizeLabel(node: NodeItem): string {
  return node.kind === 'dir' ? '—' : formatBytes(node.size)
}

interface HighlightPart {
  text: string
  hit: boolean
}

/**
 * Splits a filename around every case-insensitive occurrence of the query.
 * Rendered with text interpolation (never v-html), so a filename such as
 * `<img onerror=…>` stays literal.
 */
function highlightParts(name: string, term: string): HighlightPart[] {
  const needle = term.toLocaleLowerCase()
  if (!needle) return [{ text: name, hit: false }]

  const haystack = name.toLocaleLowerCase()
  const parts: HighlightPart[] = []
  let from = 0

  while (from <= name.length) {
    const index = haystack.indexOf(needle, from)
    if (index < 0) {
      if (from < name.length) parts.push({ text: name.slice(from), hit: false })
      break
    }
    if (index > from) parts.push({ text: name.slice(from, index), hit: false })
    parts.push({ text: name.slice(index, index + needle.length), hit: true })
    from = index + needle.length
  }

  return parts
}

interface SearchState {
  q: string
  type: SearchType
  sort: SearchSort
  page: number
}

function buildQuery(state: SearchState): LocationQueryRaw {
  const next: Record<string, string> = { q: state.q }
  if (state.type) next.type = state.type
  if (state.sort === 'recent') next.recent = '1'
  if (state.page > 1) next.page = String(state.page)
  return next
}

async function replaceState(state: SearchState): Promise<void> {
  await router.replace({ name: 'search', query: buildQuery(state) })
}

async function runSearch(): Promise<void> {
  if (!query.value) {
    search.reset()
    return
  }

  await search.search({ q: query.value, type: type.value, sort: sort.value })
  await fillPages(requestedPage.value)
  if (search.pages > 0 && search.pages !== requestedPage.value) {
    await replaceState({ q: query.value, type: type.value, sort: sort.value, page: search.pages })
  }
}

/** Loads pages until `target` is reached, stopping when the server runs out. */
async function fillPages(target: number): Promise<void> {
  let page = search.pages
  while (page < target && search.hasMore) {
    const before = search.items.length
    await search.loadMore()
    if (search.items.length === before) break
    page += 1
  }
}

async function loadMore(): Promise<void> {
  await search.loadMore()
  if (search.pages > requestedPage.value) {
    await replaceState({ q: query.value, type: type.value, sort: sort.value, page: search.pages })
  }
}

function setType(next: SearchType): void {
  if (next === type.value) return
  // Changing a filter starts a fresh result set; page depth resets.
  void replaceState({ q: query.value, type: next, sort: sort.value, page: 1 })
}

function setSort(next: SearchSort): void {
  if (next === sort.value) return
  void replaceState({ q: query.value, type: type.value, sort: next, page: 1 })
}

watch([query, type, sort], () => void runSearch(), { immediate: true })

// Back/forward can jump to a deeper page without touching q/type/sort.
watch(requestedPage, (page) => {
  if (!query.value || !search.searched || page <= search.pages) return
  void fillPages(page)
})

function openNode(node: NodeItem): void {
  if (node.kind !== 'dir') return
  void router.push({ name: 'folder', params: { id: String(node.id) } })
}

function openParent(node: NodeItem): void {
  if (node.parent_id > 0) {
    void router.push({ name: 'folder', params: { id: String(node.parent_id) } })
    return
  }
  void router.push({ name: 'files' })
}

function onRowDblClick(node: NodeItem, event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('button, a, input')) return
  if (node.kind === 'dir') openNode(node)
  else openPreview(node)
}

function openPreview(node: NodeItem): void {
  if (node.kind !== 'file') return
  previewId.value = node.id
  previewOpen.value = true
}

function closePreview(): void {
  previewOpen.value = false
  previewId.value = null
}

function onPreviewNavigate(id: number): void {
  previewId.value = id
}

function download(node: NodeItem): void {
  downloads.enqueue([{ url: downloadUrl(node.id), name: node.name, size: node.size }])
}

async function copy(node: NodeItem): Promise<void> {
  if (copyingIds.value.includes(node.id)) return
  copyingIds.value = [...copyingIds.value, node.id]
  try {
    const created = await copyNode(node.id)
    toast.push(`已复制为「${created.name}」`)
    // A copy may or may not match the query; re-running keeps the list honest.
    await runSearch()
  } catch (cause) {
    toast.push(messageForError(cause, '复制失败，请稍后重试'), 'error')
  } finally {
    copyingIds.value = copyingIds.value.filter((id) => id !== node.id)
  }
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">{{ title }}</h1>
        <p v-if="summary" class="page-sub">{{ summary }}</p>
      </div>
    </header>

    <div v-if="!query" class="panel state" role="status">
      <span class="state-art" aria-hidden="true">
        <svg width="72" height="72" viewBox="0 0 72 72" fill="none">
          <circle cx="36" cy="36" r="35" fill="var(--color-primary-soft)" />
          <circle cx="34" cy="34" r="13" stroke="var(--color-primary-ink)" stroke-width="2" />
          <path
            d="M43.5 43.5 52 52"
            stroke="var(--color-primary-ink)"
            stroke-width="2"
            stroke-linecap="round"
          />
        </svg>
      </span>
      <h2 class="state-title">搜索文件</h2>
      <p class="state-desc">在顶部搜索框输入文件名，按 Enter 开始搜索。</p>
    </div>

    <template v-else>
      <div class="toolbar">
        <div class="filters" role="group" aria-label="按类型筛选">
          <button
            v-for="filter in TYPE_FILTERS"
            :key="filter.value || 'all'"
            type="button"
            class="filter"
            :class="{ 'is-active': type === filter.value }"
            :aria-pressed="type === filter.value"
            @click="setType(filter.value)"
          >
            {{ filter.label }}
          </button>
        </div>
        <div class="filters" role="group" aria-label="排序方式">
          <button
            v-for="option in SORT_FILTERS"
            :key="option.value"
            type="button"
            class="filter"
            :class="{ 'is-active': sort === option.value }"
            :aria-pressed="sort === option.value"
            @click="setSort(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
      </div>

      <section class="panel" aria-label="搜索结果">
        <div v-if="search.loading" class="skeleton">
          <div v-for="row in 4" :key="row" class="cols skeleton-row">
            <span class="skeleton-name">
              <span class="skeleton-block skeleton-icon"></span>
              <span class="skeleton-block" :style="{ width: `${40 + (row % 3) * 15}%` }"></span>
            </span>
            <span class="col-size"><span class="skeleton-block meta-block"></span></span>
            <span class="col-date"><span class="skeleton-block date-block"></span></span>
            <span class="col-action"></span>
          </div>
          <p class="visually-hidden">搜索中…</p>
        </div>

        <div v-else-if="search.error" class="state" role="alert">
          <h2 class="state-title">搜索失败</h2>
          <p class="state-desc">{{ search.error }}</p>
          <button class="btn btn-secondary state-action" type="button" @click="void runSearch()">
            重试
          </button>
        </div>

        <div v-else-if="search.isEmpty" class="state">
          <span class="state-art" aria-hidden="true">
            <svg width="72" height="72" viewBox="0 0 72 72" fill="none">
              <circle cx="36" cy="36" r="35" fill="var(--color-primary-soft)" />
              <circle cx="34" cy="34" r="13" stroke="var(--color-primary-ink)" stroke-width="2" />
              <path
                d="M43.5 43.5 52 52"
                stroke="var(--color-primary-ink)"
                stroke-width="2"
                stroke-linecap="round"
              />
            </svg>
          </span>
          <h2 class="state-title">没有找到与 “{{ query }}” 匹配的文件</h2>
          <p class="state-desc">试试更换关键词，或把搜索词缩短一些。</p>
        </div>

        <ul v-else class="rows">
          <li
            v-for="node in search.items"
            :key="node.id"
            class="cols row"
            :class="{ 'is-dir': node.kind === 'dir' }"
            :data-node-id="node.id"
            @dblclick="onRowDblClick(node, $event)"
          >
            <span class="cell-name">
              <svg
                v-if="node.kind === 'dir'"
                class="icon icon-dir"
                width="18"
                height="18"
                viewBox="0 0 20 20"
                aria-hidden="true"
              >
                <path
                  d="M2.75 6.25A1.75 1.75 0 0 1 4.5 4.5h3.2l1.55 1.75h6.25a1.75 1.75 0 0 1 1.75 1.75v6.25a1.75 1.75 0 0 1-1.75 1.75H4.5a1.75 1.75 0 0 1-1.75-1.75V6.25Z"
                  fill="currentColor"
                  opacity="0.16"
                />
                <path
                  d="M2.75 6.25A1.75 1.75 0 0 1 4.5 4.5h3.2l1.55 1.75h6.25a1.75 1.75 0 0 1 1.75 1.75v6.25a1.75 1.75 0 0 1-1.75 1.75H4.5a1.75 1.75 0 0 1-1.75-1.75V6.25Z"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linejoin="round"
                />
              </svg>
              <svg v-else class="icon icon-file" width="18" height="18" viewBox="0 0 20 20" aria-hidden="true">
                <path
                  d="M5 3.2h6.1L15 7.1v9.4a1.3 1.3 0 0 1-1.3 1.3H5a1.3 1.3 0 0 1-1.3-1.3V4.5A1.3 1.3 0 0 1 5 3.2Z"
                  fill="currentColor"
                  opacity="0.1"
                />
                <path
                  d="M5 3.2h6.1L15 7.1v9.4a1.3 1.3 0 0 1-1.3 1.3H5a1.3 1.3 0 0 1-1.3-1.3V4.5A1.3 1.3 0 0 1 5 3.2Z"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linejoin="round"
                />
                <path
                  d="M11 3.4v3.9h3.8"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linejoin="round"
                />
              </svg>
              <button
                v-if="node.kind === 'dir'"
                class="name name-button"
                type="button"
                :title="node.name"
                @click.stop="openNode(node)"
              >
                <template v-for="(part, index) in highlightParts(node.name, search.q)" :key="index">
                  <mark v-if="part.hit" class="hit">{{ part.text }}</mark>
                  <template v-else>{{ part.text }}</template>
                </template>
              </button>
              <span v-else class="name" :title="node.name">
                <template v-for="(part, index) in highlightParts(node.name, search.q)" :key="index">
                  <mark v-if="part.hit" class="hit">{{ part.text }}</mark>
                  <template v-else>{{ part.text }}</template>
                </template>
              </span>
            </span>
            <span class="cell-meta col-size numeric">{{ sizeLabel(node) }}</span>
            <span class="cell-meta col-date numeric">{{ formatDateTime(node.updated_at) }}</span>
            <span class="col-action">
              <button
                v-if="node.kind === 'file'"
                type="button"
                class="row-action"
                :aria-label="`预览 ${node.name}`"
                title="预览"
                @click.stop="openPreview(node)"
              >
                <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                  <path
                    d="M1.8 8s2.3-4.1 6.2-4.1S14.2 8 14.2 8 11.9 12.1 8 12.1 1.8 8 1.8 8Z"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.3"
                    stroke-linejoin="round"
                  />
                  <circle cx="8" cy="8" r="1.9" fill="none" stroke="currentColor" stroke-width="1.3" />
                </svg>
              </button>
              <a
                v-if="node.kind === 'file'"
                class="row-action"
                :href="downloadUrl(node.id)"
                :download="node.name"
                :aria-label="`下载 ${node.name}`"
                title="下载"
              >
                <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                  <path
                    d="M8 2.4v7.1M5.2 7.2 8 10l2.8-2.8"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.4"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                  <path
                    d="M3.1 11.4v1.1a1.1 1.1 0 0 0 1.1 1.1h7.6a1.1 1.1 0 0 0 1.1-1.1v-1.1"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.4"
                    stroke-linecap="round"
                  />
                </svg>
              </a>
              <button
                type="button"
                class="row-action"
                :aria-label="node.kind === 'file' ? `打开 ${node.name} 所在文件夹` : `打开 ${node.name}`"
                :title="node.kind === 'file' ? '打开所在文件夹' : '打开'"
                @click.stop="node.kind === 'file' ? openParent(node) : openNode(node)"
              >
                <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                  <path
                    d="M2.2 5.2A1.3 1.3 0 0 1 3.5 3.9h2.4l1.15 1.3h4.75a1.3 1.3 0 0 1 1.3 1.3v4.7a1.3 1.3 0 0 1-1.3 1.3H3.5a1.3 1.3 0 0 1-1.3-1.3V5.2Z"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.3"
                    stroke-linejoin="round"
                  />
                  <path
                    d="M6.2 9.2h3.6M8 7.5v3.4"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.3"
                    stroke-linecap="round"
                  />
                </svg>
              </button>
              <button
                type="button"
                class="row-action"
                :aria-label="`复制 ${node.name}`"
                title="复制"
                :disabled="copyingIds.includes(node.id)"
                @click.stop="void copy(node)"
              >
                <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                  <rect
                    x="5.4"
                    y="2.4"
                    width="8.2"
                    height="9"
                    rx="1.3"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.3"
                  />
                  <path
                    d="M10.6 13.6H3.7a1.3 1.3 0 0 1-1.3-1.3V5.6"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.3"
                    stroke-linecap="round"
                  />
                </svg>
              </button>
            </span>
          </li>
        </ul>

        <div v-if="search.hasMore" class="load-more">
          <button
            class="btn btn-secondary"
            type="button"
            :disabled="search.loadingMore"
            @click="void loadMore()"
          >
            {{ search.loadingMore ? '加载中…' : `加载更多（还有 ${search.total - search.items.length} 项）` }}
          </button>
        </div>
        <p v-if="search.moreError" class="more-error" role="alert">{{ search.moreError }}</p>
      </section>
    </template>

    <PreviewOverlay
      :open="previewOpen"
      :files="previewFiles"
      :file-id="previewId"
      @close="closePreview"
      @navigate="onPreviewNavigate"
      @download="download"
    />
  </div>
</template>

<style scoped>
.page {
  max-width: var(--page-max);
  min-height: max(320px, calc(100vh - 264px));
  margin: 0 auto;
}

.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.page-heading {
  min-width: 0;
}

.page-title {
  overflow: hidden;
  font-size: var(--fs-xl);
  font-weight: 600;
  letter-spacing: -0.015em;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.page-sub {
  margin-top: 4px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.filters {
  display: flex;
  gap: 4px;
  padding: 3px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.filter {
  height: 26px;
  padding: 0 10px;
  border-radius: 5px;
  color: var(--color-muted);
  font-size: var(--fs-xs);
  font-weight: 500;
  transition:
    background-color var(--dur) var(--ease),
    color var(--dur) var(--ease);
}

.filter:hover {
  color: var(--color-text);
}

.filter.is-active {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-weight: 600;
}

.panel {
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.cols {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 88px 156px 112px;
  column-gap: 16px;
  align-items: center;
}

.rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

.row {
  min-height: 50px;
  padding: 0 16px;
  border-top: 1px solid var(--color-border);
  transition: background-color var(--dur) var(--ease);
}

.row:first-child {
  border-top: 0;
}

.row:hover {
  background: var(--color-primary-soft);
}

.row.is-dir {
  cursor: pointer;
}

.cell-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.icon {
  flex: none;
}

.icon-dir {
  color: var(--color-primary);
}

.icon-file {
  color: var(--color-muted);
}

.name {
  overflow: hidden;
  font-size: var(--fs-base);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.name-button {
  min-width: 0;
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.name-button:hover {
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.hit {
  padding: 0 2px;
  border-radius: 3px;
  background: color-mix(in srgb, var(--color-primary) 18%, transparent);
  color: var(--color-primary-ink);
  font-weight: 600;
}

.cell-meta {
  overflow: hidden;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.col-size,
.col-date {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.col-action {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 2px;
}

.row-action {
  display: grid;
  width: 26px;
  height: 26px;
  place-items: center;
  border-radius: var(--radius-sm);
  color: var(--color-muted);
  opacity: 0.65;
  transition:
    background-color var(--dur) var(--ease),
    color var(--dur) var(--ease),
    opacity var(--dur) var(--ease);
}

.row:hover .row-action,
.row-action:focus-visible {
  opacity: 1;
}

.row-action:hover {
  background: var(--color-surface);
  color: var(--color-primary-ink);
  opacity: 1;
}

.row-action:disabled {
  cursor: progress;
  opacity: 0.45;
}

.skeleton {
  padding: 0;
}

.skeleton-row {
  min-height: 50px;
  padding: 0 16px;
  border-top: 1px solid var(--color-border);
}

.skeleton-row:first-child {
  border-top: 0;
}

.skeleton-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.skeleton-block {
  display: inline-block;
  height: 10px;
  border-radius: var(--radius-pill);
  background: var(--color-border);
  animation: skeleton-pulse 1.5s var(--ease) infinite;
}

.skeleton-icon {
  flex: none;
  width: 18px;
  height: 18px;
  border-radius: 5px;
}

.meta-block {
  width: 38px;
  margin-left: auto;
}

.date-block {
  width: 104px;
  margin-left: auto;
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 54px 24px 58px;
  text-align: center;
}

.state-art {
  display: block;
  margin-bottom: 16px;
}

.state-title {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: -0.01em;
}

.state-desc {
  max-width: 360px;
  margin-top: 4px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.state-action {
  margin-top: 16px;
}

.load-more {
  display: flex;
  justify-content: center;
  padding: 14px;
  border-top: 1px solid var(--color-border);
}

.more-error {
  padding: 0 16px 14px;
  font-size: var(--fs-xs);
  color: var(--color-danger);
  text-align: center;
}

@keyframes skeleton-pulse {
  0%,
  100% {
    opacity: 0.55;
  }

  50% {
    opacity: 1;
  }
}

@media (max-width: 700px) {
  .cols {
    grid-template-columns: minmax(0, 1fr) 72px 104px;
    column-gap: 12px;
  }

  .col-date {
    display: none;
  }

  .row,
  .skeleton-row {
    padding: 0 12px;
  }

  .row-action {
    width: 24px;
    height: 24px;
  }

  .toolbar {
    flex-wrap: wrap;
  }
}
</style>
