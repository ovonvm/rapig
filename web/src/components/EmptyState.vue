<script setup lang="ts">
export type EmptyIcon = 'folder' | 'download' | 'trash' | 'share' | 'inbox'

withDefaults(
  defineProps<{
    icon?: EmptyIcon
    title?: string
    description?: string
    actionLabel?: string
    secondaryLabel?: string
  }>(),
  {
    icon: 'folder',
    title: '这里还没有文件',
    description: '新建一个文件夹，把文件整理进来。',
    actionLabel: '',
    secondaryLabel: '',
  },
)

const emit = defineEmits<{ action: []; secondary: [] }>()
</script>

<template>
  <div class="empty">
    <span class="empty-art" aria-hidden="true">
      <svg v-if="icon === 'folder'" width="112" height="80" viewBox="0 0 112 80" fill="none">
        <path
          d="M12 30a6 6 0 0 1 6-6h20.7a6 6 0 0 1 4.2 1.8l3.3 3.2H94a6 6 0 0 1 6 6v24a6 6 0 0 1-6 6H18a6 6 0 0 1-6-6V30Z"
          fill="var(--color-primary-soft)"
          stroke="var(--color-border-strong)"
          stroke-width="1.5"
          stroke-linejoin="round"
        />
        <path d="M28 47h28M28 55h18" stroke="var(--color-border-strong)" stroke-width="1.5" stroke-linecap="round" />
        <circle cx="88" cy="25" r="12.5" fill="var(--color-surface)" />
        <circle cx="88" cy="25" r="10.5" fill="var(--color-primary)" />
        <path d="M88 20.5v9M83.5 25h9" stroke="#ffffff" stroke-width="1.8" stroke-linecap="round" />
      </svg>

      <svg v-else width="72" height="72" viewBox="0 0 72 72" fill="none">
        <circle cx="36" cy="36" r="35" fill="var(--color-primary-soft)" />
        <g
          v-if="icon === 'download'"
          transform="translate(22 22)"
          stroke="var(--color-primary-ink)"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <path d="M14 3v14M8.5 12 14 17.5 19.5 12" />
          <path d="M4 21v3.5a1.6 1.6 0 0 0 1.6 1.6h16.8a1.6 1.6 0 0 0 1.6-1.6V21" />
        </g>
        <g
          v-else-if="icon === 'trash'"
          transform="translate(20 22)"
          stroke="var(--color-primary-ink)"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <path d="M4 7h24M13 7V4.2h6V7" />
          <path d="M6.5 7l1.3 17.2a1.7 1.7 0 0 0 1.7 1.6h13a1.7 1.7 0 0 0 1.7-1.6L25.5 7" />
          <path d="M14.5 12.5v8M21.5 12.5v8" />
        </g>
        <g
          v-else-if="icon === 'share'"
          transform="translate(21 22)"
          stroke="var(--color-primary-ink)"
          stroke-width="1.8"
          stroke-linecap="round"
        >
          <path d="M17.5 21.5a5.4 5.4 0 0 0 7.6 0l5.6-5.6a5.4 5.4 0 0 0-7.6-7.6l-2 2" />
          <path d="M21.5 17.5a5.4 5.4 0 0 0-7.6 0l-5.6 5.6a5.4 5.4 0 0 0 7.6 7.6l2-2" transform="translate(0 -4)" />
        </g>
        <g
          v-else
          transform="translate(22 22)"
          stroke="var(--color-primary-ink)"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <path d="M4 10h20M4 17h20M4 24h12" />
        </g>
      </svg>
    </span>

    <h2 class="empty-title">{{ title }}</h2>
    <p v-if="description" class="empty-desc">{{ description }}</p>

    <div v-if="actionLabel || secondaryLabel" class="empty-actions">
      <button v-if="actionLabel" class="btn btn-primary" type="button" @click="emit('action')">
        {{ actionLabel }}
      </button>
      <button v-if="secondaryLabel" class="btn btn-secondary" type="button" @click="emit('secondary')">
        {{ secondaryLabel }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 54px 24px 58px;
  text-align: center;
}

.empty-art {
  display: block;
  margin-bottom: 16px;
}

.empty-title {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: -0.01em;
}

.empty-desc {
  max-width: 320px;
  margin-top: 4px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}

.empty-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-top: 18px;
}
</style>
