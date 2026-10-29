<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import DownloadTaskRow from '../components/DownloadTaskRow.vue'
import EmptyState from '../components/EmptyState.vue'
import { useDownloadsStore } from '../stores/downloads'

type Filter = 'all' | 'active' | 'done' | 'failed'

const downloads = useDownloadsStore()
const filter = ref<Filter>('all')

const filtered = computed(() => {
  if (filter.value === 'active') {
    return downloads.tasks.filter(
      (task) => task.status === 'queued' || task.status === 'downloading' || task.status === 'paused',
    )
  }
  if (filter.value === 'done') return downloads.tasks.filter((task) => task.status === 'done')
  if (filter.value === 'failed') return downloads.tasks.filter((task) => task.status === 'failed')
  return downloads.tasks
})

const summary = computed(() => {
  if (!downloads.hasTasks) return ''
  return `共 ${downloads.tasks.length} 个任务，刷新页面后仍会保留`
})

onMounted(() => {
  downloads.restore()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div class="page-heading">
        <h1 class="page-title">下载中心</h1>
        <p v-if="summary" class="page-sub">{{ summary }}</p>
      </div>

      <div class="page-actions">
        <button class="btn btn-secondary" type="button" :disabled="downloads.activeCount === 0" @click="downloads.pauseAll">
          全部暂停
        </button>
        <button class="btn btn-secondary" type="button" :disabled="downloads.pausedCount === 0 && downloads.failedCount === 0" @click="downloads.resumeAll">
          全部继续
        </button>
        <button class="btn btn-secondary" type="button" :disabled="downloads.finishedCount + downloads.failedCount === 0" @click="downloads.clearFinished">
          清除已完成
        </button>
      </div>
    </header>

    <div class="tabs" role="group" aria-label="下载任务筛选">
      <button
        v-for="tab in [
          { key: 'all', label: '全部', count: downloads.tasks.length },
          { key: 'active', label: '进行中', count: downloads.activeCount + downloads.pausedCount },
          { key: 'done', label: '已完成', count: downloads.finishedCount },
          { key: 'failed', label: '失败', count: downloads.failedCount },
        ]"
        :key="tab.key"
        class="tab"
        :class="{ 'is-active': filter === tab.key }"
        type="button"
        :aria-pressed="filter === tab.key"
        @click="filter = tab.key as Filter"
      >
        {{ tab.label }}<span class="tab-count numeric">{{ tab.count }}</span>
      </button>
    </div>

    <section class="panel" aria-label="下载任务列表">
      <ul v-if="filtered.length > 0" class="task-list">
        <DownloadTaskRow
          v-for="task in filtered"
          :key="task.id"
          :task="task"
          wide
          @pause="downloads.pause"
          @resume="downloads.resume"
          @cancel="downloads.cancel"
        />
      </ul>
      <EmptyState
        v-else
        icon="download"
        :title="downloads.hasTasks ? '这个分组是空的' : '还没有下载任务'"
        :description="downloads.hasTasks ? '切换到其它分组看看。' : '在「我的文件」中下载文件后，任务会出现在这里。'"
      />
    </section>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.page-title {
  margin: 0;
  font-size: var(--fs-xl);
}

.page-sub {
  margin: 4px 0 0;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.page-actions {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  gap: 8px;
}

.tabs {
  display: flex;
  gap: 6px;
  align-items: center;
}

.tab {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  padding: 6px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  background: var(--color-surface);
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.tab:hover {
  border-color: var(--color-border-strong);
  color: var(--color-text);
}

.tab.is-active {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}

.tab-count {
  font-size: var(--fs-xs);
}

.panel {
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.task-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

@media (max-width: 700px) {
  .page-head {
    flex-direction: column;
  }

  .page-actions {
    width: 100%;
  }

  .page-actions .btn {
    flex: 1;
  }

  .tabs {
    overflow-x: auto;
    white-space: nowrap;
  }
}
</style>
