<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BatchBar from '../components/BatchBar.vue'
import DraftBar from '../components/DraftBar.vue'
import FileList from '../components/FileList.vue'
import FileGrid from '../components/FileGrid.vue'
import FolderDialog from '../components/FolderDialog.vue'
import UploadPanel from '../components/UploadPanel.vue'
import RenameDialog from '../components/RenameDialog.vue'
import MoveDialog from '../components/MoveDialog.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import ShareDialog from '../components/ShareDialog.vue'
import PreviewOverlay from '../components/PreviewOverlay.vue'
import OfflineDownloadDialog from '../components/OfflineDownloadDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import type { BatchAction } from '../components/BatchBar.vue'
import type { RowActionKind } from '../components/RowActions.vue'
import type { NodeItem, ShareInfo, TransferItem } from '../api/client'
import { downloadUrl, copyNode, restoreNode } from '../api/client'
import { errorCodeOf, isCrossPolicyError, messageForError } from '../lib/errors'
import { runWithConcurrency } from '../lib/concurrency'
import {
  FOLDER_UPLOAD_LIMIT,
  filesFromDrop,
  filesFromPicker,
  resolveFolderUpload,
} from '../lib/folderUpload'
import type { SelectedFile } from '../lib/folderUpload'
import {
  MAX_QUEUED_FILES,
  clearDrafts,
  matchDrafts,
  readDrafts,
  saveDraftList,
  saveDrafts,
  type UploadDraft,
} from '../lib/uploadDrafts'
import { pathChain, rememberPath, renamePath } from '../lib/paths'
import { useMarqueeSelect } from '../composables/useMarqueeSelect'
import { useAuthStore } from '../stores/auth'
import { useDownloadsStore } from '../stores/downloads'
import { useFilesStore } from '../stores/files'
import { useMediaStore } from '../stores/media'
import { mediaKind } from '../lib/media'
import { useTrashStore } from '../stores/trash'
import { useUploadsStore } from '../stores/uploads'
import { useToast } from '../composables/useToast'

type ViewMode = 'list' | 'grid'

const VIEW_STORAGE_KEY = 'rapig.viewMode'
/** One merged share link may contain at most this many nodes (user decision). */
const MAX_SHARE_ITEMS = 100
const BATCH_CONCURRENCY = 3
const UNDO_TOAST_MS = 10_000

const auth = useAuthStore()
const files = useFilesStore()
const trash = useTrashStore()
const downloads = useDownloadsStore()
const uploads = useUploadsStore()
const router = useRouter()
const media = useMediaStore()
const route = useRoute()
const toast = useToast()

/** 0 = drive root, otherwise the folder id from `/files/:id`. */
const parentId = computed(() => {
  const raw = route.params.id
  const value = Number(Array.isArray(raw) ? raw[0] : raw)
  return Number.isInteger(value) && value > 0 ? value : 0
})
const isRoot = computed(() => parentId.value === 0)
const crumbs = computed(() => pathChain(parentId.value))
const backCrumb = computed(() => {
  const chain = crumbs.value
  const crumb = chain.length > 1 ? chain[chain.length - 2] : undefined
  return crumb ?? null
})
const emptyTitle = computed(() => (isRoot.value ? '这里还没有文件' : '这个文件夹是空的'))
const emptyDescription = computed(() =>
  isRoot.value ? '新建一个文件夹，把文件整理进来。' : '把文件上传到这里，或者新建一个子文件夹。',
)

const dialogOpen = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const folderInput = ref<HTMLInputElement | null>(null)
const resumeInput = ref<HTMLInputElement | null>(null)
const draftInput = ref<HTMLInputElement | null>(null)
const resumeTaskId = ref<number | null>(null)
const dragDepth = ref(0)
const drafts = ref<UploadDraft | null>(readDrafts())

const renameOpen = ref(false)
const renameTarget = ref<NodeItem | null>(null)

const moveOpen = ref(false)
const moveNodes = ref<NodeItem[]>([])
const moveBusy = ref(false)
const moveError = ref<string | null>(null)
const moveStale = ref(false)

const trashOpen = ref(false)
const trashTargets = ref<NodeItem[]>([])
const trashBusy = ref(false)
const trashError = ref<string | null>(null)

const shareOpen = ref(false)
const shareTargets = ref<NodeItem[]>([])

const offlineOpen = ref(false)

const previewOpen = ref(false)
const previewId = ref<number | null>(null)
/** Files of the open directory, in list order, for previous/next navigation. */
const previewFiles = computed(() => files.items.filter((node) => node.kind === 'file'))

/** Guards against double clicking 复制 while the request is in flight. */
const copyingIds = ref<number[]>([])

const viewMode = ref<ViewMode>(readViewMode())

const selectedIds = ref<number[]>([])
const anchorIndex = ref<number | null>(null)

const selectedSet = computed(() => new Set(selectedIds.value))
const selectedNodes = computed(() => files.items.filter((node) => selectedSet.value.has(node.id)))
const selectedFiles = computed(() => selectedNodes.value.filter((node) => node.kind === 'file'))
const allSelected = computed(
  () => files.items.length > 0 && selectedIds.value.length === files.items.length,
)

const draftCount = computed(() => drafts.value?.files.length ?? 0)
const draftBytes = computed(() =>
  (drafts.value?.files ?? []).reduce((total, file) => total + file.size, 0),
)

const batchActions = computed<BatchAction[]>(() => [
  {
    key: 'download',
    label: '下载',
    icon: 'download',
    disabled: selectedFiles.value.length === 0,
    title: selectedFiles.value.length > 0 ? '加入下载队列' : '所选项目中没有文件',
  },
  {
    key: 'share',
    label: '分享',
    icon: 'share',
    disabled: selectedNodes.value.length === 0 || selectedNodes.value.length > MAX_SHARE_ITEMS,
    title:
      selectedNodes.value.length > MAX_SHARE_ITEMS
        ? `一次最多合并分享 ${MAX_SHARE_ITEMS} 项`
        : selectedNodes.value.length > 1
          ? `把选中的 ${selectedNodes.value.length} 项合并为一条分享链接`
          : '为选中项创建分享链接',
  },
  { key: 'move', label: '移动', icon: 'move' },
  { key: 'trash', label: '删除', icon: 'trash', danger: true },
])

const trashTitle = computed(() =>
  trashTargets.value.length > 1 ? `删除 ${trashTargets.value.length} 项？` : '删除文件？',
)

const trashMessage = computed(() => {
  const targets = trashTargets.value
  if (targets.length === 0) return ''
  if (targets.length === 1) return `「${targets[0]?.name ?? ''}」会移入回收站，7 天后自动彻底删除。`
  return '移入回收站，7 天后自动彻底删除。'
})

const summary = computed(() => {
  if (files.loading || files.error || files.total === 0) return ''
  const base = `共 ${files.total} 项`
  return files.items.length < files.total ? `${base}，显示前 ${files.items.length} 项` : base
})

function readViewMode(): ViewMode {
  try {
    return window.localStorage.getItem(VIEW_STORAGE_KEY) === 'grid' ? 'grid' : 'list'
  } catch {
    return 'list'
  }
}

function setViewMode(mode: ViewMode): void {
  if (viewMode.value === mode) return
  viewMode.value = mode
  try {
    window.localStorage.setItem(VIEW_STORAGE_KEY, mode)
  } catch {
    // Private mode / blocked storage: keep the choice for this session only.
  }
}

function clearSelection(): void {
  selectedIds.value = []
  anchorIndex.value = null
}

/** Navigate to a directory id (0 = drive root) without reloading the app. */
function navigateTo(id: number): void {
  if (id === parentId.value) return
  void router.push(id === 0 ? { name: 'files' } : { name: 'folder', params: { id: String(id) } })
}

/** Enter a folder from a row/card click, remembering how to name it later. */
function openFolder(node: NodeItem): void {
  if (node.kind !== 'dir' || node.id === parentId.value) return
  rememberPath(node.id, node.name, parentId.value)
  clearSelection()
  navigateTo(node.id)
}

function goBack(): void {
  const crumb = backCrumb.value
  navigateTo(crumb ? crumb.id : 0)
}

function selectAll(): void {
  selectedIds.value = files.items.map((node) => node.id)
  anchorIndex.value = files.items.length > 0 ? 0 : null
}

function indexOfNode(id: number): number {
  return files.items.findIndex((node) => node.id === id)
}

/** Replace the selection with the inclusive index range between two rows. */
function selectRange(from: number, to: number): void {
  const start = Math.min(from, to)
  const end = Math.max(from, to)
  selectedIds.value = files.items.slice(start, end + 1).map((node) => node.id)
}

/** Marquee result: keep only ids that still exist, in list order. */
function applySelection(ids: readonly number[]): void {
  const next = new Set(ids)
  selectedIds.value = files.items.filter((node) => next.has(node.id)).map((node) => node.id)
  anchorIndex.value = null
}

const marqueeRegion = ref<HTMLElement | null>(null)
const {
  rect: marqueeRect,
  dragging: marqueeDragging,
  onPointerDown: onMarqueePointerDown,
} = useMarqueeSelect({ container: marqueeRegion, selectedIds, apply: applySelection })

const marqueeStyle = computed(() => {
  const box = marqueeRect.value
  if (!box) return {}
  return {
    left: `${box.left}px`,
    top: `${box.top}px`,
    width: `${box.width}px`,
    height: `${box.height}px`,
  }
})

/**
 * Checkbox click: accumulates a toggle (no modifier needed, desktop and touch
 * alike); Shift replaces the selection with the range from the last anchor.
 */
function onToggle(id: number, event: MouseEvent): void {
  const index = indexOfNode(id)
  if (index < 0) return

  if (event.shiftKey && anchorIndex.value !== null) {
    selectRange(anchorIndex.value, index)
    return
  }

  const next = new Set(selectedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)

  selectedIds.value = files.items.filter((node) => next.has(node.id)).map((node) => node.id)
  anchorIndex.value = index
}

/** Blank row/card click: plain single selection (folders open instead). */
function onRowSelect(node: NodeItem, event: MouseEvent): void {
  const index = indexOfNode(node.id)
  if (index < 0) return

  if (event.shiftKey && anchorIndex.value !== null) {
    selectRange(anchorIndex.value, index)
    return
  }

  selectedIds.value = [node.id]
  anchorIndex.value = index
}

function onRowAction(kind: RowActionKind, node: NodeItem): void {
  if (kind === 'preview') {
    openPreview(node)
    return
  }
  if (kind === 'share') {
    openShare(node)
    return
  }
  if (kind === 'rename') {
    renameTarget.value = node
    renameOpen.value = true
    return
  }
  if (kind === 'move') {
    moveNodes.value = [node]
    moveError.value = null
    moveStale.value = false
    moveOpen.value = true
    return
  }
  if (kind === 'copy') {
    void copyNodeToSameFolder(node)
    return
  }
  trashTargets.value = [node]
  trashError.value = null
  trashOpen.value = true
}

function onRenamed(node: NodeItem): void {
  renameOpen.value = false
  if (node.kind === 'dir') renamePath(node.id, node.name)
  toast.push(`已改名为「${node.name}」`)
}

function openShare(node: NodeItem): void {
  shareTargets.value = [node]
  shareOpen.value = true
}

function openPreview(node: NodeItem): void {
  if (node.kind !== 'file') return
  if (mediaKind(node.name)) {
    media.openWith(node, files.items.filter((item) => item.kind === 'file'))
    return
  }
  previewId.value = node.id
  previewOpen.value = true
}

function closePreview(): void {
  if (!previewOpen.value) return
  previewOpen.value = false
  previewId.value = null
}

function onPreviewNavigate(id: number): void {
  previewId.value = id
}

function onPreviewDownload(node: NodeItem): void {
  enqueueDownloads([node])
}

/** One-click copy into the same folder; the server names it 「原名 - 副本」. */
async function copyNodeToSameFolder(node: NodeItem): Promise<void> {
  if (copyingIds.value.includes(node.id)) return
  copyingIds.value = [...copyingIds.value, node.id]
  try {
    const created = await copyNode(node.id)
    toast.push(`已复制为「${created.name}」`)
    await refresh()
  } catch (cause) {
    toast.push(messageForError(cause, '复制失败，请稍后重试'), 'error')
  } finally {
    copyingIds.value = copyingIds.value.filter((id) => id !== node.id)
  }
}

/** Batch bar: one item shares as a normal link, 2+ items become a merged share. */
function openShareForSelected(): void {
  const nodes = selectedNodes.value.slice()
  if (nodes.length === 0) return
  if (nodes.length > MAX_SHARE_ITEMS) {
    toast.push(`一次最多合并分享 ${MAX_SHARE_ITEMS} 项，当前已选 ${nodes.length} 项`, 'error')
    return
  }
  shareTargets.value = nodes
  shareOpen.value = true
}

function onShareCreated(share: ShareInfo): void {
  if (share.kind === 'multi') {
    toast.push(`已创建合并分享链接：共 ${share.item_count ?? shareTargets.value.length} 项`)
    return
  }
  toast.push(`已创建分享链接：${share.name}`)
}

function onMoveTo(parent: number): void {
  if (moveBusy.value) return
  const targets = moveNodes.value.slice()
  if (targets.length === 0) return

  moveBusy.value = true
  moveError.value = null
  moveStale.value = false

  if (targets.length === 1) {
    const target = targets[0]
    if (!target) {
      moveBusy.value = false
      return
    }
    files
      .moveNode(target, parent)
      .then((updated) => {
        moveOpen.value = false
        toast.push(updated.parent_id === 0 ? '已移动到「我的文件」' : `已移动「${updated.name}」`)
      })
      .catch((cause: unknown) => {
        moveError.value = messageForError(cause, '移动失败，请稍后重试')
        moveStale.value = errorCodeOf(cause) === 'stale_write'
        // M5.3: moving between storage roots is refused outright; say so loudly.
        if (isCrossPolicyError(cause)) toast.push(messageForError(cause), 'error')
      })
      .finally(() => {
        moveBusy.value = false
      })
    return
  }

  runWithConcurrency(targets, BATCH_CONCURRENCY, (node) => files.moveNode(node, parent))
    .then(async (result) => {
      const conflicts = result.failures.filter((failure) => errorCodeOf(failure.error) === 'name_conflict')
      const others = result.failures.filter((failure) => errorCodeOf(failure.error) !== 'name_conflict')
      const firstOther = others[0]

      moveOpen.value = false
      clearSelection()
      await files.load()

      const parts = [`已移动 ${result.succeeded} 项`]
      if (conflicts.length > 0) parts.push(`${conflicts.length} 项同名冲突已跳过`)
      if (firstOther) parts.push(`${others.length} 项失败：${messageForError(firstOther.error, '未知错误')}`)
      toast.push(parts.join('，'), firstOther ? 'error' : 'success')
    })
    .catch((cause: unknown) => {
      moveError.value = messageForError(cause, '移动失败，请稍后重试')
      moveStale.value = errorCodeOf(cause) === 'stale_write'
    })
    .finally(() => {
      moveBusy.value = false
    })
}

function onMoveStaleRefresh(): void {
  moveOpen.value = false
  moveStale.value = false
  void refresh()
}

function openBatchMove(): void {
  if (selectedNodes.value.length === 0) return
  moveNodes.value = selectedNodes.value.slice()
  moveError.value = null
  moveStale.value = false
  moveOpen.value = true
}

function openBatchTrash(): void {
  if (selectedNodes.value.length === 0) return
  trashTargets.value = selectedNodes.value.slice()
  trashError.value = null
  trashOpen.value = true
}

function confirmTrash(): void {
  if (trashBusy.value) return
  const targets = trashTargets.value.slice()
  if (targets.length === 0) return

  trashBusy.value = true
  trashError.value = null

  if (targets.length === 1) {
    const target = targets[0]
    if (!target) {
      trashBusy.value = false
      return
    }
    files
      .trashNode(target)
      .then(() => {
        trashOpen.value = false
        void trash.refreshTotal()
        offerUndo([target])
      })
      .catch((cause: unknown) => {
        trashError.value = messageForError(cause, '删除失败，请稍后重试')
      })
      .finally(() => {
        trashBusy.value = false
      })
    return
  }

  runWithConcurrency(targets, BATCH_CONCURRENCY, (node) => files.trashNode(node))
    .then(async (result) => {
      trashOpen.value = false
      clearSelection()
      await files.load(parentId.value)
      void trash.refreshTotal()

      const firstFailure = result.failures[0]
      if (firstFailure) {
        toast.push(
          `成功删除 ${result.succeeded} 项，失败 ${result.failures.length} 项：${messageForError(firstFailure.error, '未知错误')}`,
          'error',
        )
        return
      }
      offerUndo(targets)
    })
    .catch((cause: unknown) => {
      trashError.value = messageForError(cause, '删除失败，请稍后重试')
    })
    .finally(() => {
      trashBusy.value = false
    })
}

/** Show a 10s undo toast for a fresh delete and restore on demand. */
function offerUndo(nodes: readonly NodeItem[]): void {
  if (nodes.length === 0) return
  const label = nodes.length === 1 ? `已删除「${nodes[0]?.name ?? ''}」` : `已删除 ${nodes.length} 项`
  toast.push(label, 'undo', UNDO_TOAST_MS, {
    label: '撤销',
    run: () => {
      void undoTrash(nodes)
    },
  })
}

async function undoTrash(nodes: readonly NodeItem[]): Promise<void> {
  // Restore in reverse order so a parent directory comes back before its children.
  const targets = nodes.slice().reverse()
  const result = await runWithConcurrency(targets, BATCH_CONCURRENCY, (node) => restoreNode(node.id))
  await files.load(parentId.value)
  void trash.refreshTotal()

  const restoredIds = targets
    .filter((node) => !result.failures.some((failure) => targets[failure.index]?.id === node.id))
    .map((node) => node.id)

  if (result.failures.length > 0) {
    const first = result.failures[0]
    toast.push(
      `已撤销 ${result.succeeded} 项，${result.failures.length} 项恢复失败：${messageForError(first?.error, '未知错误')}`,
      'error',
    )
    return
  }

  selectedIds.value = restoredIds
  anchorIndex.value = null
  toast.push(targets.length === 1 ? `已恢复「${targets[0]?.name ?? ''}」` : `已恢复 ${targets.length} 项`)
}

function enqueueDownloads(nodes: readonly NodeItem[]): void {
  const targets = nodes.filter((node) => node.kind === 'file')
  if (targets.length === 0) {
    toast.push('所选内容中没有文件，文件夹不参与下载', 'error')
    return
  }

  const added = downloads.enqueue(
    targets.map((node) => ({ url: downloadUrl(node.id), name: node.name, size: node.size })),
  )
  if (added === 0) return

  toast.push(`已加入下载队列 ${added} 个文件`, 'success', 6000, {
    label: '查看任务',
    run: () => downloads.openPanel(),
  })
}

function batchDownload(): void {
  enqueueDownloads(selectedNodes.value)
}

function onBatchAction(key: string): void {
  if (key === 'download') batchDownload()
  else if (key === 'share') openShareForSelected()
  else if (key === 'move') openBatchMove()
  else if (key === 'trash') openBatchTrash()
}

/** Space downloads the selection, except while typing in a field or a dialog. */
function onWindowKeydown(event: KeyboardEvent): void {
  if (event.key !== ' ' || event.repeat || selectedIds.value.length === 0) return
  const target = event.target instanceof HTMLElement ? event.target : null
  if (target?.closest('input, textarea, select, [contenteditable="true"], .dialog')) return
  event.preventDefault()
  batchDownload()
}

/** Files beyond the first queue batch are remembered as a draft. */
function enqueueFiles(picked: readonly File[], parent: number): void {
  if (picked.length === 0) return
  if (picked.length <= MAX_QUEUED_FILES) {
    uploads.addFiles(picked.slice(), parent)
    return
  }

  uploads.addFiles(picked.slice(0, MAX_QUEUED_FILES), parent)
  saveDrafts(picked.slice(MAX_QUEUED_FILES), parent)
  drafts.value = readDrafts()
  const queued = Math.min(MAX_QUEUED_FILES, picked.length)
  toast.push(`已加入 ${queued} 个文件，剩余 ${picked.length - queued} 个文件稍后继续上传`)
}

function openDraftPicker(): void {
  draftInput.value?.click()
}

function discardDrafts(): void {
  clearDrafts()
  drafts.value = null
  toast.push('已放弃待上传的文件')
}

async function onDraftPicked(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const picked = Array.from(input.files ?? [])
  input.value = ''

  const draft = drafts.value
  if (picked.length === 0 || !draft) return

  const result = matchDrafts(picked, draft.files)
  if (result.matched.length > 0) {
    uploads.addFiles(result.matched, draft.parent > 0 ? draft.parent : parentId.value)
  }

  if (result.missing.length > 0) {
    saveDraftList(result.missing, draft.parent)
    drafts.value = readDrafts()
    toast.push(
      `已恢复 ${result.matched.length} 个文件，仍有 ${result.missing.length} 个文件没有选择`,
      result.matched.length === 0 ? 'error' : 'success',
    )
    return
  }

  clearDrafts()
  drafts.value = null
  toast.push(`已恢复 ${result.matched.length} 个待上传文件`)
}

async function refresh(): Promise<void> {
  await files.load(parentId.value)
  clearSelection()

  if (files.errorCode === 'unauthenticated') {
    auth.clearUser()
    files.reset()
    toast.push('登录状态已失效，请重新登录', 'error')
    await router.replace({ name: 'login' })
  }
}

function openDialog(): void {
  dialogOpen.value = true
}

function onCreated(name: string): void {
  dialogOpen.value = false
  toast.push(`已新建文件夹「${name}」`)
}

function openOfflineDialog(): void {
  offlineOpen.value = true
}

function onOfflineCreated(transfer: TransferItem): void {
  offlineOpen.value = false
  toast.push(`已加入离线下载：${transfer.name}`, 'success', 6000, {
    label: '查看任务',
    run: () => {
      void router.push({ name: 'transfers' })
    },
  })
}

function openPicker(): void {
  fileInput.value?.click()
}

function onPicked(event: Event): void {
  const input = event.target as HTMLInputElement
  const picked = Array.from(input.files ?? [])
  input.value = ''
  enqueueFiles(picked, parentId.value)
}

function openFolderPicker(): void {
  folderInput.value?.click()
}

async function onFolderPicked(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const picked = filesFromPicker(input.files)
  input.value = ''
  await uploadFolder(picked)
}

/** Resolve the folder tree first, then hand every file to the normal queue. */
async function uploadFolder(selected: readonly SelectedFile[], skippedEmptyDirs = 0): Promise<void> {
  if (selected.length === 0) {
    if (skippedEmptyDirs > 0) {
      toast.push('所选文件夹中没有可上传的文件（空文件夹不会被创建）', 'error')
    }
    return
  }

  if (selected.length > FOLDER_UPLOAD_LIMIT) {
    toast.push(`一次最多上传 ${FOLDER_UPLOAD_LIMIT} 个文件，请分次上传`, 'error')
    return
  }

  const resolution = await resolveFolderUpload(selected, parentId.value)
  const skipped = skippedEmptyDirs + resolution.skippedEmptyDirs

  if (resolution.entries.length > 0) uploads.enqueue(resolution.entries)

  if (resolution.failures.length > 0) {
    const first = resolution.failures[0]
    const more =
      resolution.failures.length > 1 ? `，另有 ${resolution.failures.length - 1} 个文件未加入队列` : ''
    toast.push(`${first?.path ?? '部分文件'}：${first?.message ?? '上传失败'}${more}`, 'error')
    return
  }

  if (resolution.entries.length > 0) {
    const extra = skipped > 0 ? `，跳过 ${skipped} 个空文件夹` : ''
    toast.push(`已加入 ${resolution.entries.length} 个文件${extra}`)
    return
  }

  if (skipped > 0) toast.push('所选文件夹中没有可上传的文件（空文件夹不会被创建）', 'error')
}

function openResume(id: number): void {
  resumeTaskId.value = id
  resumeInput.value?.click()
}

async function onResumePicked(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] ?? null
  const id = resumeTaskId.value
  input.value = ''
  resumeTaskId.value = null
  if (!file || id === null) return
  await uploads.resumeWithFile(id, file)
}

function carriesFiles(event: DragEvent): boolean {
  const types = event.dataTransfer?.types
  if (!types) return false
  return Array.from(types).includes('Files')
}

function onDragEnter(event: DragEvent): void {
  if (!carriesFiles(event)) return
  event.preventDefault()
  dragDepth.value += 1
}

function onDragOver(event: DragEvent): void {
  if (!carriesFiles(event)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
}

function onDragLeave(event: DragEvent): void {
  if (!carriesFiles(event)) return
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

function onDrop(event: DragEvent): void {
  if (!carriesFiles(event)) return
  event.preventDefault()
  dragDepth.value = 0

  const transfer = event.dataTransfer
  if (!transfer) return

  const fallbackFiles = Array.from(transfer.files ?? [])
  void handleDrop(transfer, fallbackFiles)
}

async function handleDrop(transfer: DataTransfer, fallbackFiles: File[]): Promise<void> {
  let dropped: { files: SelectedFile[]; skippedEmptyDirs: number } = {
    files: [],
    skippedEmptyDirs: 0,
  }

  try {
    dropped = await filesFromDrop(transfer)
  } catch {
    dropped = {
      files: fallbackFiles.map((file) => ({ file, path: file.name })),
      skippedEmptyDirs: 0,
    }
  }

  if (dropped.files.length === 0) {
    if (fallbackFiles.length > 0) enqueueFiles(fallbackFiles, parentId.value)
    return
  }

  // Plain file drops keep the flat queue; folder drops resolve their tree.
  const fromDirectory = dropped.files.some((item) => item.path.includes('/'))
  if (!fromDirectory) {
    enqueueFiles(
      dropped.files.map((item) => item.file),
      parentId.value,
    )
    return
  }

  await uploadFolder(dropped.files, dropped.skippedEmptyDirs)
}

// Never let a stray drop navigate the browser away from the SPA.
function blockWindowFileDrop(event: DragEvent): void {
  event.preventDefault()
}

// Keep the selection honest when rows disappear (trash, move, refresh).
watch(
  () => files.items,
  (items) => {
    if (selectedIds.value.length === 0) return
    const alive = new Set(items.map((node) => node.id))
    const kept = selectedIds.value.filter((id) => alive.has(id))
    if (kept.length !== selectedIds.value.length) selectedIds.value = kept
  },
  { deep: true },
)

// Browsing into another directory (or clicking the browser back button) reloads
// that directory; the breadcrumb trail survives a full page reload. The preview
// never survives a directory change or a logout.
watch(parentId, () => {
  closePreview()
  void refresh()
})

watch(
  () => auth.user,
  (user) => {
    if (!user) closePreview()
  },
)

// If the previewed file disappears (trashed, moved, renamed away) close it.
watch(
  () => previewFiles.value,
  (list) => {
    if (!previewOpen.value || previewId.value === null) return
    if (!list.some((node) => node.id === previewId.value)) closePreview()
  },
)

onMounted(() => {
  document.addEventListener('dragover', blockWindowFileDrop)
  document.addEventListener('drop', blockWindowFileDrop)
  window.addEventListener('keydown', onWindowKeydown)
  uploads.restorePersisted()
  void refresh()
})

onBeforeUnmount(() => {
  document.removeEventListener('dragover', blockWindowFileDrop)
  document.removeEventListener('drop', blockWindowFileDrop)
  window.removeEventListener('keydown', onWindowKeydown)
})
</script>

<template>
  <div
    ref="marqueeRegion"
    class="page"
    :class="{ 'is-marquee': marqueeDragging }"
    @pointerdown="onMarqueePointerDown"
    @dragenter.prevent="onDragEnter"
    @dragover.prevent="onDragOver"
    @dragleave.prevent="onDragLeave"
    @drop.prevent="onDrop"
  >
    <header class="page-head">
      <div class="page-heading">
        <button v-if="!isRoot" class="back-link" type="button" @click="goBack">
          <svg width="13" height="13" viewBox="0 0 14 14" aria-hidden="true">
            <path
              d="M8.6 3.4 4.8 7l3.8 3.6"
              fill="none"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          返回上一级
        </button>

        <nav class="breadcrumb" aria-label="面包屑">
          <template v-for="(crumb, index) in crumbs" :key="crumb.id">
            <span v-if="index > 0" class="crumb-sep" aria-hidden="true">/</span>
            <h1
              v-if="index === crumbs.length - 1"
              class="page-title crumb-current"
              aria-current="page"
            >
              {{ crumb.name }}
            </h1>
            <button v-else class="crumb-link" type="button" @click="navigateTo(crumb.id)">
              {{ crumb.name }}
            </button>
          </template>
        </nav>

        <p v-if="summary" class="page-sub">{{ summary }}</p>
      </div>

      <div class="page-actions">
        <button class="btn btn-secondary" type="button" @click="openDialog">
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <path d="M7 2.5v9M2.5 7h9" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
          </svg>
          新建文件夹
        </button>

        <button class="btn btn-secondary" type="button" @click="openFolderPicker">
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <path
              d="M2.1 5.1a1.3 1.3 0 0 1 1.3-1.3h2.2l1.1 1.3h3.9a1.3 1.3 0 0 1 1.3 1.3v4.1a1.3 1.3 0 0 1-1.3 1.3H3.4a1.3 1.3 0 0 1-1.3-1.3V5.1Z"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linejoin="round"
            />
            <path
              d="M7 9.4V5.6M5.6 6.9 7 5.6l1.4 1.3"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          上传文件夹
        </button>

        <button class="btn btn-secondary" type="button" @click="openOfflineDialog">
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <path
              d="M4.4 10.6a2.4 2.4 0 0 1-.3-4.8 3.4 3.4 0 0 1 6.4-.5 2.5 2.5 0 0 1 .6 4.9"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
            <path
              d="M7 6.2v5M5.2 9.4 7 11.2l1.8-1.8"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          离线下载
        </button>

        <button class="btn btn-primary" type="button" @click="openPicker">
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
            <path
              d="M7 9.4V2.6M4.1 5.5 7 2.6l2.9 2.9"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
            <path
              d="M2.4 9.8v.6a1.2 1.2 0 0 0 1.2 1.2h6.8a1.2 1.2 0 0 0 1.2-1.2v-.6"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
            />
          </svg>
          上传文件
        </button>

        <div class="segmented" role="group" aria-label="视图切换">
          <button
            class="segmented-btn"
            :class="{ 'is-active': viewMode === 'list' }"
            type="button"
            :aria-pressed="viewMode === 'list'"
            @click="setViewMode('list')"
          >
            <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
              <path
                d="M2 3.5h10M2 7h10M2 10.5h10"
                fill="none"
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
              />
            </svg>
            列表
          </button>
          <button
            class="segmented-btn"
            :class="{ 'is-active': viewMode === 'grid' }"
            type="button"
            :aria-pressed="viewMode === 'grid'"
            @click="setViewMode('grid')"
          >
            <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
              <path
                d="M2.2 2.2h3.4v3.4H2.2zM8.4 2.2h3.4v3.4H8.4zM2.2 8.4h3.4v3.4H2.2zM8.4 8.4h3.4v3.4H8.4z"
                fill="currentColor"
              />
            </svg>
            网格
          </button>
        </div>
      </div>
    </header>

    <input ref="fileInput" class="visually-hidden" type="file" multiple tabindex="-1" @change="onPicked" />
    <input
      ref="folderInput"
      class="visually-hidden"
      type="file"
      webkitdirectory
      multiple
      tabindex="-1"
      @change="onFolderPicked"
    />
    <input
      ref="resumeInput"
      class="visually-hidden"
      type="file"
      tabindex="-1"
      aria-label="选择要继续上传的文件"
      @change="onResumePicked"
    />

    <input
      ref="draftInput"
      class="visually-hidden"
      type="file"
      multiple
      tabindex="-1"
      aria-label="选择要继续上传的文件"
      @change="onDraftPicked"
    />

    <UploadPanel @resume="openResume" />

    <BatchBar
      :count="selectedIds.length"
      :total="files.items.length"
      :actions="batchActions"
      :all-selected="allSelected"
      @action="onBatchAction"
      @clear="clearSelection"
      @select-all="selectAll"
    />

    <DraftBar
      :count="draftCount"
      :bytes="draftBytes"
      :raised="selectedIds.length > 0"
      @resume="openDraftPicker"
      @discard="discardDrafts"
    />

    <FileList
      v-if="viewMode === 'list'"
      :items="files.items"
      :loading="files.loading"
      :error="files.error"
      :selected-ids="selectedSet"
      :empty-title="emptyTitle"
      :empty-description="emptyDescription"
      @retry="refresh"
      @create="openDialog"
      @action="onRowAction"
      @toggle="onToggle"
      @select="onRowSelect"
      @open="openFolder"
      @preview="openPreview"
    />

    <template v-else>
      <section class="panel" aria-label="文件网格">
        <div v-if="files.loading" class="grid-skeleton" aria-hidden="true">
          <div v-for="card in 6" :key="card" class="grid-skeleton-card">
            <span class="skeleton-block grid-skeleton-icon"></span>
            <span class="skeleton-block" :style="{ width: `${50 + (card % 3) * 12}%` }"></span>
            <span class="skeleton-block" style="width: 42px"></span>
          </div>
          <p class="visually-hidden">加载中…</p>
        </div>

        <div v-else-if="files.error" class="state" role="alert">
          <h2 class="state-title">加载失败</h2>
          <p class="state-desc">{{ files.error }}</p>
          <button class="btn btn-secondary state-action" type="button" @click="refresh">重试</button>
        </div>

        <EmptyState
          v-else-if="files.items.length === 0"
          :title="emptyTitle"
          :description="emptyDescription"
          @action="openDialog"
        />
        <FileGrid
          v-else
          :items="files.items"
          :selected-ids="selectedSet"
          @toggle="onToggle"
          @select="onRowSelect"
          @action="onRowAction"
          @open="openFolder"
          @preview="openPreview"
        />
      </section>
    </template>

    <FolderDialog :open="dialogOpen" :at-root="isRoot" @close="dialogOpen = false" @created="onCreated" />
    <RenameDialog
      :open="renameOpen"
      :node="renameTarget"
      @close="renameOpen = false"
      @renamed="onRenamed"
      @refresh="refresh"
    />
    <MoveDialog
      :open="moveOpen"
      :nodes="moveNodes"
      :busy="moveBusy"
      :error="moveError"
      :stale="moveStale"
      @close="moveOpen = false"
      @move-to="onMoveTo"
      @refresh="onMoveStaleRefresh"
    />
    <ConfirmDialog
      :open="trashOpen"
      :title="trashTitle"
      :message="trashMessage"
      detail="可在回收站中恢复。"
      confirm-label="删除"
      danger
      :busy="trashBusy"
      :error="trashError"
      @close="trashOpen = false"
      @confirm="confirmTrash"
    />
    <ShareDialog
      :open="shareOpen"
      :node="shareTargets.length === 1 ? shareTargets[0]! : null"
      :nodes="shareTargets"
      @close="shareOpen = false"
      @created="onShareCreated"
    />
    <OfflineDownloadDialog
      :open="offlineOpen"
      :parent="parentId"
      :folder-label="crumbs[crumbs.length - 1]?.name ?? '我的文件'"
      @close="offlineOpen = false"
      @created="onOfflineCreated"
    />
    <PreviewOverlay
      :open="previewOpen"
      :files="previewFiles"
      :file-id="previewId"
      @close="closePreview"
      @navigate="onPreviewNavigate"
      @download="onPreviewDownload"
    />

    <div v-if="marqueeRect" class="marquee" :style="marqueeStyle" aria-hidden="true"></div>

    <div v-if="dragDepth > 0" class="drop-overlay" aria-hidden="true">
      <div class="drop-card">
        <svg width="34" height="34" viewBox="0 0 34 34">
          <path
            d="M17 24V10.5M11.4 16.1 17 10.5l5.6 5.6"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
          <path
            d="M6.5 22.6v3.2a1.7 1.7 0 0 0 1.7 1.7h17.6a1.7 1.7 0 0 0 1.7-1.7v-3.2"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
          />
        </svg>
        <p class="drop-text">松开鼠标即可上传</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  position: relative;
  max-width: var(--page-max);
  min-height: max(320px, calc(100vh - 264px));
  margin: 0 auto;
}

/* Marquee selection: the drag paints over the list without stealing clicks. */
.page.is-marquee,
.page.is-marquee * {
  user-select: none;
}

.marquee {
  position: absolute;
  z-index: 6;
  border: 1px solid var(--color-primary);
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-primary) 12%, transparent);
  pointer-events: none;
}

.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.page-title {
  font-size: var(--fs-xl);
  font-weight: 600;
  letter-spacing: -0.015em;
  line-height: 1.3;
}

.page-sub {
  margin-top: 2px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.page-heading {
  display: flex;
  min-width: 0;
  flex-direction: column;
}

.back-link {
  display: inline-flex;
  align-self: flex-start;
  align-items: center;
  gap: 3px;
  margin-bottom: 5px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  transition: color var(--dur) var(--ease);
}

.back-link:hover {
  color: var(--color-text);
}

/* The breadcrumb doubles as the page title; long paths scroll inside it. */
.breadcrumb {
  display: flex;
  max-width: 100%;
  align-items: baseline;
  gap: 5px;
  overflow-x: auto;
  white-space: nowrap;
  scrollbar-width: none;
}

.breadcrumb::-webkit-scrollbar {
  display: none;
}

.crumb-sep {
  flex: none;
  color: var(--color-border-strong);
  font-size: var(--fs-lg);
}

.crumb-link {
  flex: none;
  padding: 0;
  border: 0;
  background: none;
  color: var(--color-muted);
  font-size: var(--fs-lg);
  font-weight: 500;
  letter-spacing: -0.01em;
  transition: color var(--dur) var(--ease);
}

.crumb-link:hover {
  color: var(--color-primary-ink);
}

.crumb-current {
  flex: none;
}

.page-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.segmented {
  display: inline-flex;
  align-items: center;
  padding: 2px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.segmented-btn {
  display: inline-flex;
  height: 28px;
  align-items: center;
  gap: 5px;
  padding: 0 9px;
  border-radius: 5px;
  color: var(--color-muted);
  font-size: var(--fs-sm);
  transition:
    background-color var(--dur) var(--ease),
    color var(--dur) var(--ease);
}

.segmented-btn:hover {
  color: var(--color-text);
}

.segmented-btn.is-active {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.selection-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  padding: 8px 12px;
  background: var(--color-primary-soft);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.selection-info {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}

.selection-count {
  font-size: var(--fs-sm);
  font-weight: 600;
}

.link-button {
  font-size: var(--fs-sm);
  color: var(--color-primary-ink);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.link-button:disabled {
  color: var(--color-muted);
  text-decoration: none;
  cursor: default;
}

.selection-actions {
  display: flex;
  gap: 6px;
}

.selection-actions .btn {
  height: 30px;
  padding: 0 11px;
}

.panel {
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.grid-skeleton {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 12px;
  padding: 16px;
}

.grid-skeleton-card {
  display: flex;
  min-height: 138px;
  flex-direction: column;
  gap: 10px;
  padding: 14px 13px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.grid-skeleton-icon {
  width: 30px;
  height: 30px;
  border-radius: 7px;
}

.skeleton-block {
  display: inline-block;
  height: 10px;
  border-radius: var(--radius-pill);
  background: var(--color-border);
  animation: skeleton-pulse 1.5s var(--ease) infinite;
}

.state {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 52px 24px 56px;
  text-align: center;
}

.state-title {
  font-size: 15px;
  font-weight: 600;
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

@keyframes skeleton-pulse {
  0%,
  100% {
    opacity: 0.55;
  }

  50% {
    opacity: 1;
  }
}

.drop-overlay {
  position: fixed;
  inset: 0;
  z-index: 40;
  display: grid;
  place-items: center;
  padding: 24px;
  background: color-mix(in srgb, var(--color-bg) 72%, transparent);
  pointer-events: none;
}

.drop-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 36px 52px;
  border: 2px dashed var(--color-primary);
  border-radius: var(--radius);
  background: var(--color-surface);
  color: var(--color-primary-ink);
  text-align: center;
}

.drop-text {
  font-size: var(--fs-base);
  font-weight: 600;
}

@media (max-width: 760px) {
  .page-head {
    flex-wrap: wrap;
    align-items: center;
  }

  .page-actions {
    width: 100%;
    flex-wrap: wrap;
  }

  .page-actions .btn {
    flex: 1;
    justify-content: center;
  }
}

@media (max-width: 520px) {
  .selection-bar {
    flex-wrap: wrap;
  }

  .selection-actions {
    width: 100%;
  }

  .selection-actions .btn {
    flex: 1;
    justify-content: center;
  }
}
</style>
