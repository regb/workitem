// A tab outlives the build that served it: the page loads parts of itself on
// demand (a route, the review panel, a highlighting language) from files whose
// names carry a content hash, and a newer wi web no longer has the old ones.
// Failing to load one means the tab is stale: it reloads, once, to the build
// now served. A reload that fails the same way within a short while is not
// repeated, so a broken build shows its error instead of looping.

const key = 'wi.staleBuildReload'
const quietFor = 30_000

// staleBuild reports a failure to load part of the page.
export function staleBuild(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error)
  return /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS/i.test(message)
}

// reloadForNewBuild reloads a stale tab, unless it just did; it reports
// whether it is reloading.
export function reloadForNewBuild(error: unknown, now = Date.now()): boolean {
  if (!staleBuild(error)) return false
  try {
    const last = Number(sessionStorage.getItem(key) ?? 0)
    if (now - last < quietFor) return false
    sessionStorage.setItem(key, String(now))
  } catch {
    return false
  }
  location.reload()
  return true
}

// loadFresh loads part of the page, reloading a stale tab instead of failing.
// While the reload happens the load never settles, so nothing shows an error.
export function loadFresh<T>(load: () => Promise<T>): () => Promise<T> {
  return () => load().catch(error => (reloadForNewBuild(error) ? new Promise<T>(() => {}) : Promise.reject(error)))
}
