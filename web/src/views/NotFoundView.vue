<script setup lang="ts">
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
</script>

<template>
  <main class="notfound-page">
    <div class="notfound-card">
      <p class="notfound-code" aria-hidden="true">404</p>
      <h1 class="notfound-title">页面不存在</h1>
      <p class="notfound-desc">这个地址没有对应的页面，可能已经移动了。</p>
      <RouterLink class="btn btn-secondary" :to="auth.isAuthenticated ? '/files' : '/login'">
        {{ auth.isAuthenticated ? '返回我的文件' : '去登录' }}
      </RouterLink>
    </div>
  </main>
</template>

<style scoped>
.notfound-page {
  display: grid;
  min-height: 100%;
  padding: 24px;
  place-items: center;
}

.notfound-card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  width: min(420px, 100%);
  padding: 28px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
}

.notfound-code {
  font-size: 38px;
  font-weight: 600;
  line-height: 1;
  letter-spacing: -0.03em;
  color: var(--color-border-strong);
}

.notfound-title {
  margin-top: 14px;
  font-size: var(--fs-lg);
  font-weight: 600;
}

.notfound-desc {
  margin: 6px 0 20px;
  font-size: var(--fs-sm);
  color: var(--color-muted);
}
</style>
