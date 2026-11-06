import { afterEach, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AccessLevel } from '@/gen/wi/core/v1/items_pb'
import { wireMention } from '@/lib/item-mentions'
import { AccessRequests, AgentAccess, MentionNote, grantPending, levelText, mentionedItems, pendingFor } from './mention-access'

// Radix's select scrolls and captures the pointer, which jsdom lacks.
Element.prototype.scrollIntoView ??= () => {}
Element.prototype.hasPointerCapture ??= () => false
Element.prototype.releasePointerCapture ??= () => {}

const calls: { method: string; request: unknown }[] = []
let levels: Record<string, AccessLevel> = {}
let asked: { itemId: string; needed: AccessLevel; refusals: number }[] = []
let grants: { id: string; subject: string; itemId: string; level: AccessLevel }[] = []
const titles: Record<string, string> = { '01J0ABCDEFGHJKMNPQRSTVWXY2': 'Granted earlier' }
vi.mock('@/lib/rpc', async original => ({
  ...(await original<object>()),
  items: {
    listAccess: async () => ({ entries: grants }),
    listItems: async () => ({ items: [{ id: 'delegated-1', title: 'Sub task', state: 2 }] }),
    listAccessRequests: async () => ({ requests: asked }),
    dismissAccessRequest: async (request: unknown) => {
      calls.push({ method: 'dismissAccessRequest', request })
      return {}
    },
    getItem: async (request: { itemId: string }) => ({ item: { id: request.itemId, title: titles[request.itemId] ?? request.itemId } }),
    checkAccess: async (request: { itemIds: string[] }) => {
      calls.push({ method: 'checkAccess', request })
      return { access: request.itemIds.map(itemId => ({ itemId, level: levels[itemId] ?? AccessLevel.UNSPECIFIED })) }
    },
    grantAccess: async (request: { onItemId: string; level: AccessLevel }) => {
      calls.push({ method: 'grantAccess', request })
      levels[request.onItemId] = request.level
      return {}
    },
    revokeAccess: async (request: unknown) => {
      calls.push({ method: 'revokeAccess', request })
      return {}
    },
  },
}))

const other = { id: '01J0ABCDEFGHJKMNPQRSTVWXYZ', title: 'Fix [login]', slug: 'fix-login' }
const self = '01J0ABCDEFGHJKMNPQRSTVWXY1'

afterEach(() => {
  cleanup()
  calls.length = 0
  levels = {}
  asked = []
  grants = []
})

const withClient = (node: React.ReactNode) => render(<QueryClientProvider client={new QueryClient()}>{node}</QueryClientProvider>)

// Note renders the bubble's content with the pending choices a composer
// keeps.
let chosen: Record<string, AccessLevel> = {}
function Note({ subject }: { subject: string }) {
  const [pending, setPending] = useState<Record<string, AccessLevel>>({})
  chosen = pending
  return <MentionNote subject={subject} item={other} pending={pending} onPending={setPending} />
}

it('reads the mentioned items once each, and keeps choices only for them', () => {
  const text = `Look at ${wireMention(other)}, then ${wireMention(other)} and ${wireMention({ id: self, title: 'Me' })}`
  expect(mentionedItems(text, self)).toEqual([{ id: other.id, title: 'Fix [login]' }])
  expect(pendingFor(text, self, { [other.id]: AccessLevel.MESSAGE, gone: AccessLevel.CONTROL })).toEqual({ [other.id]: AccessLevel.MESSAGE })
})

it('says what each level lets the agent do', () => {
  expect(levelText(AccessLevel.VIEW)).toBe('can read it')
  expect(levelText(AccessLevel.MESSAGE)).toBe('can message and steer it')
  expect(levelText(AccessLevel.MANAGE)).toBe('manages it')
})

it('tells that mentioning gives read access, and grants a level chosen above it only when sent', async () => {
  withClient(<Note subject={`item:${self}`} />)
  expect(await screen.findByText('Mentioning Fix [login] lets this agent read it')).toBeTruthy()
  fireEvent.click(screen.getByRole('combobox', { name: 'What this agent may do to Fix [login]' }))
  fireEvent.click(await screen.findByRole('option', { name: 'Let it message and steer it' }))
  await waitFor(() => expect(screen.getByText('Sending lets this agent message and steer it')).toBeTruthy())
  expect(calls.some(call => call.method === 'grantAccess')).toBe(false)
  await grantPending(self, chosen)
  expect(calls).toContainEqual({ method: 'grantAccess', request: { subject: `item:${self}`, onItemId: other.id, level: AccessLevel.MESSAGE } })
})

it('shows the access the agent has, offering only more', async () => {
  levels = { [other.id]: AccessLevel.MESSAGE }
  withClient(<Note subject={`item:${self}`} />)
  expect(await screen.findByText('This agent can message and steer it')).toBeTruthy()
  fireEvent.click(screen.getByRole('combobox'))
  expect((await screen.findAllByRole('option')).map(option => option.textContent)).toEqual(['It can message and steer it', 'Let it drive its agent'])
  cleanup()
  levels = { [other.id]: AccessLevel.CONTROL }
  withClient(<Note subject={`item:${self}`} />)
  expect(await screen.findByText('This agent can drive its agent')).toBeTruthy()
  expect(screen.queryByRole('combobox')).toBeNull()
})

it("tells a new item's agent will read what its prompt mentions", () => {
  withClient(<Note subject="" />)
  expect(screen.getByText("Mentioning Fix [login] lets the new item's agent read it")).toBeTruthy()
})

it("lists, in details, all the agent's access: through ownership, its own entries to change or revoke, and Settings' entries", async () => {
  const earlier = '01J0ABCDEFGHJKMNPQRSTVWXY2'
  grants = [
    { id: 'e1', subject: `item:${self}`, itemId: earlier, level: AccessLevel.VIEW },
    { id: 'e2', subject: `item:${self}`, itemId: '*', level: AccessLevel.VIEW },
    { id: 'e3', subject: 'item:*', itemId: other.id, level: AccessLevel.VIEW },
  ]
  withClient(<AgentAccess itemId={self} />)
  expect(await screen.findByText('Granted earlier')).toBeTruthy()
  expect(await screen.findByText('The item it delegated, and theirs')).toBeTruthy()
  expect(screen.getByText('This item')).toBeTruthy()
  expect(screen.getByText('Every item')).toBeTruthy()
  expect(screen.getByText("May read: every item's agent, in Settings")).toBeTruthy()
  expect(screen.getAllByRole('listitem')).toHaveLength(5)
  fireEvent.click(screen.getByRole('combobox', { name: 'What this agent may do to Granted earlier' }))
  fireEvent.click(await screen.findByRole('option', { name: 'May message and steer' }))
  await waitFor(() => expect(calls).toContainEqual({ method: 'grantAccess', request: { subject: `item:${self}`, onItemId: earlier, level: AccessLevel.MESSAGE } }))
  fireEvent.click(screen.getByRole('button', { name: 'Revoke access to Granted earlier' }))
  await waitFor(() => expect(calls).toContainEqual({ method: 'revokeAccess', request: { entryId: 'e1' } }))
})

it('asks you to allow what the agent was refused', async () => {
  asked = [{ itemId: other.id, needed: AccessLevel.MESSAGE, refusals: 2 }]
  titles[other.id] = 'Fix login'
  withClient(<AccessRequests itemId={self} />)
  expect(await screen.findByText(/This agent asked to message and steer/)).toBeTruthy()
  expect(screen.getByText(/refused 2 times/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
  await waitFor(() => expect(calls).toContainEqual({ method: 'grantAccess', request: { subject: `item:${self}`, onItemId: other.id, level: AccessLevel.MESSAGE } }))
})
