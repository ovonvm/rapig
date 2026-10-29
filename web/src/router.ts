import { createRouter, createWebHistory } from 'vue-router'
import type { RouteLocationNormalized } from 'vue-router'
import { useAuthStore } from './stores/auth'

declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    guestOnly?: boolean
    layout?: 'shell'
    adminOnly?: boolean
    /** System admin or a user with `can_create_groups` (P1 group console). */
    groupsAdmin?: boolean
  }
}

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: '/files' },
    {
      path: '/login',
      name: 'login',
      component: () => import('./views/LoginView.vue'),
      meta: { guestOnly: true },
    },
    {
      path: '/register',
      name: 'register',
      component: () => import('./views/RegisterView.vue'),
      meta: { guestOnly: true },
    },
    {
      path: '/files',
      name: 'files',
      component: () => import('./views/FilesView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/files/:id(\\d+)',
      name: 'folder',
      component: () => import('./views/FilesView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/search',
      name: 'search',
      component: () => import('./views/SearchView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      // Shared group space (P1): the `id` is the group id, not a node id.
      path: '/groups/:id(\\d+)',
      name: 'group',
      component: () => import('./views/GroupFilesView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/trash',
      name: 'trash',
      component: () => import('./views/TrashView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/downloads',
      name: 'downloads',
      component: () => import('./views/DownloadView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/transfers',
      name: 'transfers',
      component: () => import('./views/TransfersView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/shares',
      name: 'shares',
      component: () => import('./views/SharesView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('./views/SettingsView.vue'),
      meta: { requiresAuth: true, layout: 'shell' },
    },
    {
      path: '/admin/users',
      name: 'admin-users',
      component: () => import('./views/AdminUsersView.vue'),
      meta: { requiresAuth: true, layout: 'shell', adminOnly: true },
    },
    {
      path: '/admin/audit',
      name: 'admin-audit',
      component: () => import('./views/AdminAuditView.vue'),
      meta: { requiresAuth: true, layout: 'shell', adminOnly: true },
    },
    {
      path: '/admin/settings',
      name: 'admin-settings',
      component: () => import('./views/AdminSettingsView.vue'),
      meta: { requiresAuth: true, layout: 'shell', adminOnly: true },
    },
    {
      path: '/admin/storage',
      name: 'admin-storage',
      component: () => import('./views/AdminStorageView.vue'),
      meta: { requiresAuth: true, layout: 'shell', adminOnly: true },
    },
    {
      // Group console (P1): system admins see every group, delegates only theirs.
      path: '/admin/groups',
      name: 'admin-groups',
      component: () => import('./views/AdminGroupsView.vue'),
      meta: { requiresAuth: true, layout: 'shell', groupsAdmin: true },
    },
    {
      path: '/s/:token',
      name: 'share',
      component: () => import('./views/PublicShareView.vue'),
    },
    {
      // Anonymous, one-time account-deletion cancel link (M5.2c).
      path: '/cancel-deletion',
      name: 'cancel-deletion',
      component: () => import('./views/CancelDeletionView.vue'),
    },
    {
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: () => import('./views/NotFoundView.vue'),
    },
  ],
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to: RouteLocationNormalized) => {
  const auth = useAuthStore()

  // One /auth/me on startup; routes decide what an anonymous visitor sees.
  await auth.ensureSession()

  if (to.meta.requiresAuth && !auth.isAuthenticated) {
    const redirect = to.fullPath === '/files' ? {} : { redirect: to.fullPath }
    return { name: 'login', query: redirect }
  }

  if (to.meta.guestOnly && auth.isAuthenticated) {
    return { name: 'files' }
  }

  if (to.meta.adminOnly && !auth.isAdmin) {
    return { name: 'files' }
  }

  if (to.meta.groupsAdmin && !auth.canManageGroups) {
    return { name: 'files' }
  }

  return true
})
