import { BotIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

// Drivers by name, with how the page shows them. The marks are simple
// stand-ins drawn here, not the vendors' own logos.
export const driverNames: Record<string, string> = { claude: 'Claude Code', pi: 'Pi', antigravity: 'Antigravity' }

// DriverLogo marks which coding agent runs a profile or conversation.
export function DriverLogo({ driver, className }: { driver?: string; className?: string }) {
  const size = cn('size-4 shrink-0', className)
  if (driver === 'claude') return <ClaudeMark className={size} />
  if (driver === 'pi') return <PiMark className={size} />
  if (driver === 'antigravity') return <AntigravityMark className={size} />
  return <BotIcon className={size} aria-hidden />
}

// ClaudeMark is a sunburst in Claude's orange.
function ClaudeMark({ className }: { className?: string }) {
  const rays = Array.from({ length: 12 }, (_, i) => i * 30)
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden data-driver="claude">
      {rays.map(angle => (
        <line key={angle} x1="12" y1="12" x2="12" y2={angle % 60 === 0 ? 2.5 : 4.5} stroke="#D97757" strokeWidth="2.4" strokeLinecap="round" transform={`rotate(${angle} 12 12)`} />
      ))}
    </svg>
  )
}

// PiMark is a π on a rounded badge.
function PiMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden data-driver="pi">
      <rect x="1" y="1" width="22" height="22" rx="6" fill="currentColor" opacity="0.18" />
      <path d="M6 8.5h12M9.5 8.5v8M14.5 8.5v6.2c0 1.1.6 1.8 1.8 1.8" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  )
}

// AntigravityMark is an upward chevron in Google blue.
function AntigravityMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden data-driver="antigravity">
      <path d="M4 18 12 5l8 13" fill="none" stroke="#4285F4" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M8.5 18 12 12.3l3.5 5.7" fill="none" stroke="#34A853" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
