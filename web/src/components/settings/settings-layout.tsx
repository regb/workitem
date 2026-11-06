import { useEffect, useRef, type ComponentType, type ReactNode } from 'react'
import { Link, type LinkProps } from '@tanstack/react-router'
import { cn } from '@/lib/utils'

export type SettingsNavEntry = { label: string; icon: ComponentType<{ className?: string }>; link: LinkProps; current: boolean }

// SettingsLayout is a settings area: a header naming what is configured, a
// sidebar of its sections (a row of links on phones), and the section.
export function SettingsLayout({ header, nav, title, children }: { header: ReactNode; nav: SettingsNavEntry[]; title: string; children: ReactNode }) {
  // On a phone the links scroll sideways: keep the current one in view.
  const links = useRef<HTMLElement>(null)
  const current = nav.find(entry => entry.current)?.label
  useEffect(() => {
    links.current?.querySelector('[aria-current="page"]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [current])
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6 px-4 py-6">
        <header className="border-b pb-4">{header}</header>
        <div className="flex flex-col gap-6 md:flex-row md:gap-8">
          <nav ref={links} aria-label="Settings sections" className="flex shrink-0 gap-1 overflow-x-auto md:w-52 md:flex-col">
            {nav.map(entry => (
              <Link
                key={entry.label}
                {...entry.link}
                aria-current={entry.current ? 'page' : undefined}
                className={cn(
                  'flex shrink-0 items-center gap-2 rounded-md px-2.5 py-1.5 text-sm whitespace-nowrap text-muted-foreground transition-colors hover:bg-muted hover:text-foreground',
                  entry.current && 'bg-muted font-medium text-foreground',
                )}
              >
                <entry.icon className="size-4" />
                {entry.label}
              </Link>
            ))}
          </nav>
          <section aria-labelledby="settings-title" className="flex min-w-0 flex-1 flex-col gap-5">
            <h2 id="settings-title" className="border-b pb-2 text-lg font-semibold">
              {title}
            </h2>
            {children}
          </section>
        </div>
      </div>
    </div>
  )
}
