<script setup lang="ts">
import { formatBytes } from '../lib/format'

withDefaults(
  defineProps<{
    count: number
    bytes: number
    /** Lift the bar above an open batch bar so both stay usable. */
    raised?: boolean
  }>(),
  { raised: false },
)

const emit = defineEmits<{ resume: []; discard: [] }>()
</script>

<template>
  <Teleport to="body">
    <Transition name="draft">
      <div
        v-if="count > 0"
        class="draft-bar"
        :class="{ 'is-raised': raised }"
        role="status"
        aria-label="待上传文件"
      >
        <svg class="draft-icon" width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
          <path
            d="M2.8 4.6a1.3 1.3 0 0 1 1.3-1.3h2.2l1.1 1.3h3.7a1.3 1.3 0 0 1 1.3 1.3v5.6a1.3 1.3 0 0 1-1.3 1.3H4.1a1.3 1.3 0 0 1-1.3-1.3V4.6Z"
            fill="none"
            stroke="currentColor"
            stroke-width="1.3"
            stroke-linejoin="round"
          />
          <path d="M8 10.2V6.6M6.6 7.9 8 6.5l1.4 1.4" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        <span class="draft-text">
          <strong class="draft-count">{{ count }} 个文件待上传</strong>
          <span class="draft-meta numeric">{{ formatBytes(bytes) }}</span>
        </span>
        <button class="btn btn-primary draft-resume" type="button" @click="emit('resume')">
          选择文件继续上传
        </button>
        <button class="draft-discard" type="button" @click="emit('discard')">放弃</button>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.draft-bar {
  position: fixed;
  bottom: calc(18px + env(safe-area-inset-bottom, 0px));
  left: 50%;
  z-index: 44;
  display: flex;
  gap: 12px;
  align-items: center;
  max-width: calc(100vw - 24px);
  padding: 8px 10px 8px 14px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  transform: translateX(-50%);
}

.draft-bar.is-raised {
  bottom: calc(80px + env(safe-area-inset-bottom, 0px));
}

.draft-icon {
  flex: none;
  color: var(--color-primary-ink);
}

.draft-text {
  display: flex;
  gap: 8px;
  align-items: baseline;
  white-space: nowrap;
}

.draft-count {
  font-size: var(--fs-sm);
  font-weight: 500;
}

.draft-meta {
  font-size: var(--fs-xs);
  color: var(--color-muted);
}

.draft-resume {
  height: 30px;
  padding: 0 12px;
  font-size: var(--fs-sm);
}

.draft-discard {
  padding: 4px 6px;
  border: 0;
  background: none;
  font-size: var(--fs-sm);
  color: var(--color-muted);
  cursor: pointer;
}

.draft-discard:hover {
  color: var(--color-text);
}

.draft-enter-active,
.draft-leave-active {
  transition:
    opacity 160ms var(--ease),
    transform 160ms var(--ease);
}

.draft-enter-from,
.draft-leave-to {
  opacity: 0;
  transform: translate(-50%, 8px);
}

@media (max-width: 640px) {
  .draft-bar {
    right: 12px;
    left: 12px;
    flex-wrap: wrap;
    transform: none;
  }

  .draft-resume {
    flex: 1;
  }

  .draft-enter-from,
  .draft-leave-to {
    transform: translateY(8px);
  }
}
</style>
