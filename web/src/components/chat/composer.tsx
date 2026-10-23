import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import { ArrowUpIcon, SquareIcon } from 'lucide-react'
import { useChoose, useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { AttachButton, AttachedImages, useImageAttachments } from './image-attachments'
import { MentionField } from './mention-field'
import { ModelPicker } from './model-picker'
import { stopTurn, type Delivery } from '@/lib/actions'
import type { Chat } from '@/lib/api'
import { knownMatches, type OutboxMessage } from '@/lib/history'
import { touchKeyboard } from '@/lib/keys'
import { forgetMessages, shownMessages, usePendingMessages, useSendMessage, type SentMessage } from '@/lib/send'
import { newClientID } from '@/lib/session'
import { drafts, saveMessage } from '@/lib/drafts'
import { clearError } from '@/lib/ui-state'

export type ComposerHandle = { restore: (message: OutboxMessage) => void }

export const Composer = forwardRef<ComposerHandle, { chat: Chat }>(function Composer({ chat }, ref) {
  const confirm = useConfirm()
  const choose = useChoose()
  const id = chat.item.id
  // The draft survives leaving the conversation; item mentions in it are
  // kept in their sent form.
  const [text, setText] = useState(() => drafts.messages.get(id) ?? '')
  const [delivery, setDelivery] = useState<Delivery>('prompt')
  const send = useSendMessage(id, confirm, choose)
  const pending = send.isPending
  // While the agent works, an empty box offers to stop it, as in ChatGPT;
  // typing turns the button back into send, to queue a follow-up.
  const sending = usePendingMessages(id).some(message => message.status === 'sending')
  const agentWorking = chat.item.state === 'active' && (chat.status === 'busy' || sending)
  const [stopping, setStopping] = useState(false)
  const stop = async () => {
    setStopping(true)
    await stopTurn(confirm, id)
    setStopping(false)
  }
  const input = useRef<HTMLTextAreaElement>(null)
  const capabilities = chat.runtime.capabilities
  const attached = useImageAttachments(!!capabilities.image_input)
  const images = attached.images

  useImperativeHandle(ref, () => ({
    restore(message) {
      changeText(message.text)
      attached.restore(message.images)
      forgetMessages(id, new Set([message.client_id]))
      input.current?.focus()
    },
  }))

  // Opening a conversation is for writing to it: with a keyboard, typing goes
  // straight to the message box. Touch devices are left alone, as focusing
  // would pop up their keyboard over the conversation.
  useEffect(() => {
    if (!touchKeyboard()) input.current?.focus({ preventScroll: true })
  }, [])

  const changeText = (value: string) => {
    setText(value)
    saveMessage(id, value)
  }

  // Sending takes control: an offline or terminal-only runtime is restarted
  // headless, so only an online structured runtime limits delivery modes.
  const structured = chat.control?.status.channels.control === 'structured'
  // Send now means now: to a working agent that can be steered, the message
  // joins the turn in progress. Queue next waits for the turn to end.
  const steering = delivery === 'prompt' && agentWorking && chat.runtime.online && !!capabilities.steering
  const sent: Delivery = steering ? 'steer' : delivery
  const supported = !chat.runtime.online || !structured || (sent === 'prompt' ? capabilities.prompt : sent === 'steer' ? capabilities.steering : capabilities.follow_up)
  // The composer shows for active and backlog items. A message to a backlog
  // item starts it, with the message as its prompt.
  const active = chat.item.state === 'active'
  const backlog = chat.item.state === 'backlog'
  const canSend = !pending && !!supported && (!!text.trim() || images.length > 0)

  // The message shows at once as a pending send; history confirms it later.
  const submit = () => {
    if (!canSend) return
    const message: SentMessage = {
      client_id: newClientID(),
      text: text.trim(),
      images: attached.sent(),
      known: knownMatches(shownMessages.get(id) ?? chat.history.messages, text),
      delivery: sent,
      runtime_id: chat.runtime.runtime?.id ?? '',
    }
    changeText('')
    attached.clear()
    clearError()
    send.mutate(message)
  }

  return (
    <form
      className="mx-auto w-full max-w-3xl px-3 pb-3 sm:px-4"
      onSubmit={event => {
        event.preventDefault()
        submit()
      }}
    >
      <div className="relative flex flex-col rounded-xl border bg-input/20 focus-within:border-ring">
        <AttachedImages images={images} onRemove={attached.remove} />
        <MentionField
          ref={input}
          aria-label="Message"
          placeholder={backlog ? 'Message the agent to start this item' : 'Message agent'}
          value={text}
          onValueChange={changeText}
          onSubmit={submit}
          files={`/api/items/${id}/files`}
          exclude={id}
          box="px-3.5 py-3 text-base md:text-sm"
          rows={3}
          onPaste={attached.paste}
          className="max-h-[40vh] min-h-24 resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 dark:bg-transparent"
        />
        <div className="flex items-center gap-1 p-1.5">
          {capabilities.image_input && <AttachButton onFiles={files => void attached.add(files)} disabled={pending} />}
          <span className="flex-1" />
          <ModelPicker id={id} disabled={!active} />
          <Select value={delivery} onValueChange={value => setDelivery(value as Delivery)}>
            <SelectTrigger aria-label="Message delivery" size="sm" className="border-0 bg-transparent text-muted-foreground shadow-none dark:bg-transparent">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="prompt">{steering ? 'Steer now' : 'Send now'}</SelectItem>
              <SelectItem value="follow_up">Queue next</SelectItem>
            </SelectContent>
          </Select>
          {agentWorking && !text.trim() && !images.length ? (
            <Button type="button" size="icon-sm" aria-label="Stop the agent" title="Stop the agent" disabled={stopping} onClick={stop}>
              <SquareIcon className="fill-current" />
            </Button>
          ) : (
            <Button type="submit" size="icon-sm" aria-label="Send message" disabled={!canSend}>
              <ArrowUpIcon />
            </Button>
          )}
        </div>
      </div>
    </form>
  )
})
