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

export const router = createRouter({ routeTree: rootRoute.addChildren([listRoute, newRoute, chatRoute]) })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
