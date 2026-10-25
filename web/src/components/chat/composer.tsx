import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import { ArrowUpIcon, ChevronDownIcon, CornerDownRightIcon, ForwardIcon, ListPlusIcon, SquareIcon } from 'lucide-react'
import { useChoose, useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuShortcut, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useMoveOn } from '@/hooks/use-move-on'
import { AttachButton, AttachedImages, useImageAttachments } from './image-attachments'
import { MentionField } from './mention-field'
import { findModel, ModelPicker, shownSettings } from './model-picker'
import { stopTurn, type Delivery } from '@/lib/actions'
import type { AgentSettings, Chat } from '@/lib/api'
import { knownMatches, type OutboxMessage } from '@/lib/history'
import { useAgentSettings } from '@/lib/queries'
import { heldKeys, shortcut, touchKeyboard, useHeldKeys, type HeldKeys } from '@/lib/keys'
import { forgetMessages, shownMessages, usePendingMessages, useSendMessage, type SentMessage } from '@/lib/send'
import { newClientID } from '@/lib/session'
import { drafts, saveMessage } from '@/lib/drafts'
import { clearError } from '@/lib/ui-state'

export type ComposerHandle = { restore: (message: OutboxMessage) => void }

// How a message is sent: the default for the agent's state, queued for the
// end of the turn, or sent before moving on to the next item.
type SendAction = 'send' | 'queue' | 'next'

const queueKeys = { mod: true }
const nextKeys = { mod: true, alt: true }

// The send button's mode, chosen from its caret, is kept in this browser.
// Enter ignores it.
const modeKey = 'wi.send-mode'
function readMode(): SendAction {
  try {
    const mode = localStorage.getItem(modeKey)
    return mode === 'queue' || mode === 'next' ? mode : 'send'
  } catch {
    return 'send'
  }
}
function saveMode(mode: SendAction) {
  try {
    localStorage.setItem(modeKey, mode)
  } catch {
    // The mode lasts for this page only.
  }
}

export const Composer = forwardRef<ComposerHandle, { chat: Chat }>(function Composer({ chat }, ref) {
  const confirm = useConfirm()
  const choose = useChoose()
  const id = chat.item.id
  // The draft survives leaving the conversation; item mentions in it are
  // kept in their sent form.
  const [text, setText] = useState(() => drafts.messages.get(id) ?? '')
  const send = useSendMessage(id, confirm, choose)
  const { goNext } = useMoveOn(id)
  const held = useHeldKeys()
  const [mode, setMode] = useState(readMode)
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
  const { data: settings } = useAgentSettings(id)
  const imageInput = chat.runtime.online ? !!capabilities.image_input : startsWithImages(settings)
  const attached = useImageAttachments(imageInput)
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
  // Sending means now: to a working agent that can be steered, the message
  // joins the turn in progress. Queueing waits for the turn to end, so it is
  // offered only while the agent works.
  const steering = agentWorking && chat.runtime.online && !!capabilities.steering
  const delivery = (action: SendAction): Delivery => (action === 'queue' ? 'follow_up' : steering ? 'steer' : 'prompt')
  const supported = (sent: Delivery) => !chat.runtime.online || !structured || !!(sent === 'prompt' ? capabilities.prompt : sent === 'steer' ? capabilities.steering : capabilities.follow_up)
  const canQueue = agentWorking && supported('follow_up')
  // Enter sends; with ⌘ it queues, and with ⌘⌥ it sends and moves on.
  const actionFor = (keys: HeldKeys): SendAction => (keys.mod && keys.alt ? 'next' : keys.mod && canQueue ? 'queue' : 'send')
  // A click does what the button's mode says, unless modifiers say otherwise.
  // Queueing to an idle agent would wait forever, so it sends instead.
  const clickAction = (keys: HeldKeys): SendAction => (keys.mod ? actionFor(keys) : mode === 'queue' && !canQueue ? 'send' : mode)
  // The composer shows for active and backlog items. A message to a backlog
  // item starts it, with the message as its prompt.
  const backlog = chat.item.state === 'backlog'
  const hasContent = !!text.trim() || images.length > 0
  const canSend = (action: SendAction) => !pending && hasContent && supported(delivery(action))

  // The message shows at once as a pending send; history confirms it later.
  // Moving on waits until the server took the message, so a refusal is seen
  // here.
  const submit = (action: SendAction) => {
    if (!canSend(action)) return
    const message: SentMessage = {
      client_id: newClientID(),
      text: text.trim(),
      images: attached.sent(),
      known: knownMatches(shownMessages.get(id) ?? chat.history.messages, text),
      delivery: delivery(action),
      runtime_id: chat.runtime.runtime?.id ?? '',
    }
    changeText('')
    attached.clear()
    clearError()
    send.mutate(message, action === 'next' ? { onSuccess: () => void goNext() } : undefined)
  }
  const label = (action: SendAction) => (action === 'next' ? 'Send and go to next' : action === 'queue' ? 'Queue next' : steering ? 'Steer now' : 'Send')
  const shown = clickAction(held)
  const icon = (action: SendAction) => (action === 'next' ? <ForwardIcon /> : action === 'queue' ? <ListPlusIcon /> : steering ? <CornerDownRightIcon /> : <ArrowUpIcon />)
  const hint = [
    `Click to ${label(shown).toLowerCase()}`,
    `${shortcut('Enter')} to ${label('send').toLowerCase()}`,
    canQueue && `${shortcut('Enter', queueKeys)} or ${shortcut('click', queueKeys)} to queue next`,
    `${shortcut('Enter', nextKeys)} or ${shortcut('click', nextKeys)} to send and go to next`,
  ].filter((line): line is string => !!line)
  // A touch screen has no shortcuts to show.
  const keys = !touchKeyboard()

  return (
    <form
      className="mx-auto w-full max-w-3xl px-3 pb-3 sm:px-4"
      onSubmit={event => {
        event.preventDefault()
        submit('send')
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
          onSubmit={keys => submit(actionFor(keys))}
          files={`/api/items/${id}/files`}
          exclude={id}
          box="px-3.5 py-3 text-base md:text-sm"
          rows={3}
          onPaste={attached.paste}
          className="max-h-[40vh] min-h-24 resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 dark:bg-transparent"
        />
        <div className="flex items-center gap-1 p-1.5">
          {imageInput && <AttachButton onFiles={files => void attached.add(files)} disabled={pending} />}
          <span className="flex-1" />
          <ModelPicker id={id} />
          {/* One button, joined to a caret that sets what a click does: the
              only way to queue or move on from a touch screen. */}
          <div className="flex">
            {agentWorking && !hasContent ? (
              <Button type="button" size="icon-sm" className="rounded-e-none" aria-label="Stop the agent" title="Stop the agent" disabled={stopping} onClick={stop}>
                <SquareIcon className="fill-current" />
              </Button>
            ) : (
              <Tooltip delayDuration={500}>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    size="icon-sm"
                    className="rounded-e-none"
                    aria-label={label(shown)}
                    data-send-action={shown}
                    disabled={!canSend(shown)}
                    onClick={event => submit(clickAction(heldKeys(event)))}
                  >
                    {icon(shown)}
                  </Button>
                </TooltipTrigger>
                {keys && (
                  <TooltipContent side="top" align="end" collisionPadding={8}>
                    <span className="flex flex-col gap-0.5">
                      {hint.map(line => (
                        <span key={line}>{line}</span>
                      ))}
                    </span>
                  </TooltipContent>
                )}
              </Tooltip>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button type="button" size="icon-sm" aria-label="Choose what the send button does" className="w-5 rounded-s-none border-s border-s-primary-foreground/25">
                  <ChevronDownIcon className="size-3" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" side="top" className="w-max whitespace-nowrap">
                <DropdownMenuLabel className="text-xs text-muted-foreground">Send button</DropdownMenuLabel>
                <DropdownMenuRadioGroup
                  value={mode}
                  onValueChange={value => {
                    setMode(value as SendAction)
                    saveMode(value as SendAction)
                  }}
                >
                  {(['send', 'queue', 'next'] as const).map(action => (
                    <DropdownMenuRadioItem key={action} value={action}>
                      {icon(action)} {action === 'send' ? (steering ? 'Send or steer' : 'Send') : label(action)}
                      {keys && <DropdownMenuShortcut className="ps-6">{shortcut('Enter', action === 'queue' ? queueKeys : action === 'next' ? nextKeys : {})}</DropdownMenuShortcut>}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </div>
    </form>
  )
})

// startsWithImages says whether an agent that is not running, such as a
// backlog item's, will read images: its model's catalog entry says (the
// field is left out when false), and an unknown model is given the benefit
// of the doubt, as the agent checks on delivery.
export function startsWithImages(settings: AgentSettings | undefined) {
  const model = settings && findModel(settings, shownSettings(settings, settings.running).model)
  return model ? !!model.image_input : true
}
