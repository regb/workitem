// Resolve calendar choices in the browser's timezone, never on the server.
export function waitInstant(choice: string, now = new Date()): string {
  let date: Date
  if (choice === '1h' || choice === '4h') {
    date = new Date(now.getTime() + (choice === '1h' ? 1 : 4) * 3600000)
  } else if (choice === 'tomorrow' || choice === 'monday') {
    date = new Date(now)
    const days = choice === 'tomorrow' ? 1 : (8 - now.getDay()) % 7 || 7
    date.setDate(date.getDate() + days)
    date.setHours(9, 0, 0, 0)
  } else {
    // datetime-local input is intentionally interpreted in local time.
    date = new Date(choice)
  }
  if (!Number.isFinite(date.getTime()) || date <= now) throw new Error('Choose a future time.')
  const offset = -date.getTimezoneOffset()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}${offset < 0 ? '-' : '+'}${pad(Math.floor(Math.abs(offset) / 60))}:${pad(Math.abs(offset) % 60)}`
}
