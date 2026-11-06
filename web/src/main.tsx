import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { ConfirmProvider } from '@/components/confirm'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { queryClient } from '@/lib/queries'
import { router } from '@/router'
import { reloadForNewBuild } from '@/lib/stale-build'
import './index.css'

// Vite reports a part of the page it could not load: the tab is stale.
window.addEventListener('vite:preloadError', event => {
  if (reloadForNewBuild((event as Event & { payload?: unknown }).payload)) event.preventDefault()
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
          <Toaster position="bottom-right" />
        </ConfirmProvider>
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
)
