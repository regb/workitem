import { useMutation, useMutationState, type Mutation } from '@tanstack/react-query'
import type { Choice, ConfirmOptions } from '@/components/confirm'
import { sendMessage, type Delivery } from './actions'
import { ApiError, type HistoryMessage } from './api'
import type { OutboxMessage } from './history'
import { queryClient, refreshChat } from './queries'
import { reportError } from './ui-state'

// A sent message is a mutation keyed by its item. Its variables are the
// message as the page shows it until native history confirms it; then, or
// when the operator takes it back to edit, the mutation is removed.
export type SentMessage = Omit<OutboxMessage, 'status'> & { delivery: Delivery; runtime_id: string }

const sendKey = (item: string) => ['send', item] as const

// status reads a send's outcome: a refusal by the server means not sent; a
// connection that dropped first leaves delivery unknown.
function status(mutation: Mutation<unknown, Error, SentMessage>): OutboxMessage['status'] {
  switch (mutation.state.status) {
    case 'success':
      return 'sent'
    case 'error':
      return mutation.state.error instanceof ApiError && mutation.state.error.outcome ? 'failed' : 'unknown'
    default:
      return 'sending'
  }
}

export function useSendMessage(item: string, confirm: (options: ConfirmOptions) => Promise<boolean>, choose: (options: ConfirmOptions & { secondary: string }) => Promise<Choice>) {
  return useMutation({
    mutationKey: sendKey(item),
    mutationFn: (message: SentMessage) =>
      sendMessage(confirm, choose, item, {
        text: message.text,
        images: message.images.map(image => ({ mime_type: image.mime_type, data: image.data })),
        delivery: message.delivery,
        runtime_id: message.runtime_id,
        client_id: message.client_id,
      }),
    // Kept until history confirms the message, however long that takes.
    gcTime: Infinity,
    onError: error =>
      reportError(error instanceof ApiError && error.outcome ? error.message : 'The connection ended before delivery was confirmed. The message is still shown; edit and retry only after checking the conversation.'),
    onSettled: () => void refreshChat(item),
  })
}

// usePendingMessages lists an item's sent messages history does not show yet.
export function usePendingMessages(item: string): OutboxMessage[] {
  return useMutationState({
    // A mutation joins the cache before its variables are set.
    filters: { mutationKey: sendKey(item), predicate: mutation => mutation.state.variables !== undefined },
    select: mutation => {
      const sent = mutation as Mutation<unknown, Error, SentMessage>
      const { delivery: _delivery, runtime_id: _runtime, ...message } = sent.state.variables!
      return { ...message, status: status(sent) }
    },
  })
}

// forgetMessages removes sent messages history now shows, or that the
// operator took back to edit.
export function forgetMessages(item: string, clientIDs: Set<string>) {
  const cache = queryClient.getMutationCache()
  for (const mutation of cache.findAll({ mutationKey: sendKey(item) })) {
    if (clientIDs.has((mutation.state.variables as SentMessage | undefined)?.client_id ?? '')) cache.remove(mutation)
  }
}

// The messages each open conversation shows, so a message being sent can
// record which equal messages already exist and must not confirm it.
export const shownMessages = new Map<string, HistoryMessage[]>()
