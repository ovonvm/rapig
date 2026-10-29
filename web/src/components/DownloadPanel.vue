<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import { storeToRefs } from 'pinia'
import DownloadTaskRow from './DownloadTaskRow.vue'
import { useDownloadsStore } from '../stores/downloads'

const downloads = useDownloadsStore()
const { panelOpen, hasTasks, activeCount, pausedCount, finishedCount, failedCount, tasks } =
  storeToRefs(downloads)
const { closePanel, pauseAll, resumeAll, clearFinished } = downloads

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && downloads.panelOpen) downloads.closePanel()
}

watch(
  () => downloads.panelOpen,
  (open) => {
    if (open) window.addEventListener('keydown', onKeydown)
    else window.removeEventListener('keydown', onKeydown)
  },
  { immediate: true },
)

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <Teleport to="body">
    <Transition name="panel">
      <section v-if="panelOpen" class="download-panel" role="dialog" aria-label="下载任务">
        <header class="panel-head">
          <div class="panel-title">
            下载任务
            <span v-if="activeCount > 0" class="panel-count">{{ activeCount }} 个进行中</span>
            <span v-else-if="pausedCount > 0" class="panel-count">{{ pausedCount }} 个已暂停</span>
          </div>
          <button class="panel-close" type="button" aria-label="关闭下载面板" @click="closePanel">
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
              <path d="m2.2 2.2 5.6 5.6M7.8 2.2 2.2 7.8" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
            </svg>
          </button>
        </header>

        <div v-if="hasTasks" class="panel-tools">
          <button class="panel-link" type="button" :disabled="activeCount === 0 && pausedCount === 0" @click="pauseAll">
            全部暂停
          </button>
          <button class="panel-link" type="button" :disabled="pausedCount === 0 && failedCount === 0" @click="resumeAll">
            全部继续
          </button>
          <button class="panel-link" type="button" :disabled="finishedCount + failedCount === 0" @click="clearFinished">
            清除已完成
          </button>
        </div>

        <ul v-if="hasTasks" class="panel-list">
          <DownloadTaskRow
            v-for="task in tasks"
            :key="task.id"
            :task="task"
            @pause="downloads.pause"
            @resume="downloads.resume"
            @cancel="downloads.cancel"
          />
        </ul>
        <p v-else class="panel-empty">还没有下载任务</p>

        <footer v-if="hasTasks" class="panel-foot">
          <RouterLink class="panel-link" to="/downloads" @click="closePanel">查看所有任务</RouterLink>
        </footer>
      </section>
    </Transition>
  </Teleport>
</template>

<style scoped>
.download-panel {
  position: fixed;
  right: 18px;
  bottom: 18px;
  z-index: 55;
  display: flex;
  flex-direction: column;
  width: min(368px, calc(100vw - 24px));
  max-height: min(460px, calc(100vh - 120px));
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
}

.panel-head {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--color-border);
}

.panel-title {
  display: flex;
  gap: 8px;
  align-items: baseline;
  font-size: var(--fs-sm);
  font-weight: 600;
}

.panel-count {
  font-size: var(--fs-xs);
  font-weight: 400;
  color: var(--color-muted);
}

.panel-close {
  display: grid;
  flex: none;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 6px;
  color: var(--color-muted);
}

.panel-close:hover {
  background: var(--color-bg);
  color: var(--color-text);
}

.panel-tools {
  display: flex;
  flex: none;
  gap: 12px;
  padding: 8px 14px;
  border-bottom: 1px solid var(--color-border);
}

.panel-link {
  padding: 0;
  border: 0;
  background: none;
  font-size: var(--fs-xs);
  color: var(--color-primary-ink);
  cursor: pointer;
}

.panel-link:hover:not(:disabled) {
  text-decoration: underline;
  text-underline-offset: 2px;
}

.panel-link:disabled {
  color: var(--color-muted);
  opacity: 0.6;
  cursor: not-allowed;
}

.panel-list {
  flex: 1;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
}

.panel-empty {
  padding: 32px 16px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  text-align: center;
}

.panel-foot {
  flex: none;
  padding: 9px 14px;
  border-top: 1px solid var(--color-border);
  text-align: right;
}

.panel-enter-active,
.panel-leave-active {
  transition:
    opacity 160ms var(--ease),
    transform 160ms var(--ease);
}

.panel-enter-from,
.panel-leave-to {
  opacity: 0;
  transform: translateY(8px);
}

@media (max-width: 640px) {
  .download-panel {
    right: 12px;
    bottom: 12px;
    left: 12px;
    width: auto;
  }
}
</style>
