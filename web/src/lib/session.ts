// Per-tab session: the access token from the launch URL and a client ID.
// Each tab is its own control client, so two tabs never share a lease.

function storage(): Storage | undefined {
  try {
    return window.sessionStorage
  } catch {
    return undefined
  }
}

function readToken(): string {
  const hash = new URLSearchParams(location.hash.slice(1))
  const fromURL = hash.get('token')
  if (fromURL) {
    storage()?.setItem('wi-token', fromURL)
    history.replaceState(null, '', location.pathname + location.search)
  }
  return storage()?.getItem('wi-token') ?? ''
}

// randomID is 32 random hex digits. crypto.randomUUID would need a secure
// context, which a plain-http tailnet address is not.
function randomID(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return [...bytes].map(b => b.toString(16).padStart(2, '0')).join('')
}

function readClientID(): string {
  const saved = storage()?.getItem('wi-client') ?? ''
  if (/^[a-f0-9-]{8,64}$/.test(saved)) return saved
  const value = randomID()
  storage()?.setItem('wi-client', value)
  return value
}

export const token = readToken()
export const clientID = readClientID()
// newClientID names a message or creation the page sends, so the server can
// tell a retry from a new request.
export const newClientID = randomID
