import { createRootRoute, createRoute, createRouter, lazyRouteComponent, redirect } from '@tanstack/react-router'
import { RootLayout } from '@/components/root-layout'
import { WorkListPage } from '@/pages/work-list-page'

const rootRoute = createRootRoute({ component: RootLayout })

const listRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  // Links from the earlier client used ?item=ID and ?new=1.
  beforeLoad: ({ location }) => {
    const search = new URLSearchParams(location.searchStr)
    const item = search.get('item')
    if (item) throw redirect({ to: '/items/$id', params: { id: item } })
    if (search.has('new')) throw redirect({ to: '/new' })
  },
  component: WorkListPage,
})

// New work starts from the dashboard; /new remains for older links.
const newRoute = createRoute({ getParentRoute: () => rootRoute, path: '/new', beforeLoad: () => { throw redirect({ to: '/' }) } })

// The chat page carries Markdown and highlighting; load it on first use.
const chatRoute = createRoute({ getParentRoute: () => rootRoute, path: '/items/$id', component: lazyRouteComponent(() => import('@/pages/chat-page'), 'ChatPage') })

// Settings: Wi's own at /settings, and each repository's apart at
// /repositories/ID/settings, as on GitHub.
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', beforeLoad: ({ location }) => { if (location.pathname.replace(/\/$/, '') === '/settings') throw redirect({ to: '/settings/$section', params: { section: 'general' } }) } })
const settingsSectionRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings/$section', component: lazyRouteComponent(() => import('@/pages/settings-page'), 'SettingsPage') })
const repositorySettingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/repositories/$repo/settings', beforeLoad: ({ params, location }) => { if (location.pathname.replace(/\/$/, '').endsWith('/settings')) throw redirect({ to: '/repositories/$repo/settings/$section', params: { repo: params.repo, section: 'general' } }) } })
const repositorySettingsSectionRoute = createRoute({ getParentRoute: () => rootRoute, path: '/repositories/$repo/settings/$section', component: lazyRouteComponent(() => import('@/pages/repository-settings-page'), 'RepositorySettingsPage') })

export const router = createRouter({ routeTree: rootRoute.addChildren([listRoute, newRoute, chatRoute, settingsRoute, settingsSectionRoute, repositorySettingsRoute, repositorySettingsSectionRoute]) })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
