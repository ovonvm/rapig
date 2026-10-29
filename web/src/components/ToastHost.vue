<script setup lang="ts">
import { useToast } from '../composables/useToast'

const { toasts, dismiss, runAction } = useToast()
</script>

<template>
  <div class="toast-host" aria-live="polite" aria-atomic="false">
    <TransitionGroup name="toast">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        class="toast"
        :class="`toast-${toast.tone}`"
        :role="toast.tone === 'error' ? 'alert' : 'status'"
      >
        <svg v-if="toast.tone === 'success'" class="toast-icon" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
          <path
            d="m3 7.4 2.6 2.6L11 4.3"
            fill="none"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
        <svg
          v-else-if="toast.tone === 'undo'"
          class="toast-icon"
          width="14"
          height="14"
          viewBox="0 0 14 14"
          aria-hidden="true"
        >
          <path
            d="M4 7.4a3.6 3.6 0 1 0 1.1-2.6M3.8 2.7v2.9h2.9"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
        <svg v-else class="toast-icon" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
          <path d="M7 4.2v3.6M7 10.2v.1" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" />
          <circle cx="7" cy="7" r="5.6" fill="none" stroke="currentColor" stroke-width="1.3" opacity="0.5" />
        </svg>
        <span class="toast-message">{{ toast.message }}</span>
        <button
          v-if="toast.action"
          class="toast-action"
          type="button"
          @click="runAction(toast)"
        >
          {{ toast.action.label }}
        </button>
        <button class="toast-close" type="button" aria-label="关闭提示" @click="dismiss(toast.id)">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <path
              d="m2.2 2.2 5.6 5.6M7.8 2.2 2.2 7.8"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
            />
          </svg>
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-host {
  position: fixed;
  /* Keep toasts clear of the floating batch bar and draft bar. */
  bottom: calc(96px + env(safe-area-inset-bottom, 0px));
  left: 50%;
  z-index: 60;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  width: min(440px, calc(100vw - 32px));
  transform: translateX(-50%);
  pointer-events: none;
}

.toast {
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 100%;
  padding: 8px 10px 8px 13px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  box-shadow: var(--shadow-pop);
  font-size: var(--fs-sm);
  pointer-events: auto;
}

.toast-icon {
  flex: none;
  color: var(--color-success);
}

.toast-undo .toast-icon {
  color: var(--color-primary-ink);
}

.toast-error .toast-icon {
  color: var(--color-danger);
}

.toast-message {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.toast-action {
  flex: none;
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  background: var(--color-surface);
  font-size: var(--fs-xs);
  font-weight: 500;
  color: var(--color-primary-ink);
  cursor: pointer;
}

.toast-action:hover {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}

.toast-close {
  display: grid;
  flex: none;
  place-items: center;
  width: 20px;
  height: 20px;
  margin-left: 2px;
  border-radius: 50%;
  color: var(--color-muted);
}

.toast-close:hover {
  background: var(--color-bg);
  color: var(--color-text);
}

.toast-enter-active,
.toast-leave-active {
  transition:
    opacity 160ms var(--ease),
    transform 160ms var(--ease);
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(6px);
}
</style>
