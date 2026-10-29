<script setup lang="ts">
import { useRoute } from 'vue-router'
import AppShell from './components/AppShell.vue'
import ToastHost from './components/ToastHost.vue'

const route = useRoute()
</script>

<template>
  <AppShell v-if="route.meta.layout === 'shell'" />
  <RouterView v-else />
  <ToastHost />
</template>
