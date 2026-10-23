// Actions are colored by meaning: finishing work is green, deferring it blue,
// attention amber; each brightens with a tinted background on hover.
export const tone = {
  done: 'text-emerald-400 hover:bg-emerald-500/15 hover:text-emerald-300 dark:hover:bg-emerald-500/15',
  wait: 'text-sky-400 hover:bg-sky-500/15 hover:text-sky-300 dark:hover:bg-sky-500/15 aria-expanded:bg-sky-500/15',
  // Starting work is offered, not just available: tinted before any hover.
  start: 'border-sky-500/50 bg-sky-500/15 text-sky-300 hover:bg-sky-500/25 hover:text-sky-200 dark:border-sky-500/50 dark:bg-sky-500/15 dark:hover:bg-sky-500/25',
  neutral: 'text-muted-foreground hover:bg-foreground/10 hover:text-foreground dark:hover:bg-foreground/10 aria-expanded:bg-foreground/10',
}

// sectionTone colors a dashboard section's name, which is also an item's
// status: needs attention amber, needs fixing red, in progress green.
export const sectionTone: Record<string, string> = {
  'NEEDS ATTENTION': 'text-amber-300',
  'NEEDS FIXING': 'text-red-300',
  'IN PROGRESS': 'text-emerald-300',
}
