import { useState } from 'react'
import { useIsMutating, useMutation } from '@tanstack/react-query'
import { GitMergeIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { ApiError, itemAction, type Chat, type Outcome } from '@/lib/api'
import { reviewUI, setMergeMessage } from '@/lib/review'

// mergeBlocker says why the item cannot merge now, if it cannot.
export function mergeBlocker(chat: Chat, unsavedFiles: boolean): string {
  if (chat.item.state !== 'active') return 'Only an active item can merge.'
  if (!chat.item.checkout.claim) return 'The item has no checkout to merge.'
  if (chat.status === 'busy') return 'The agent is running a turn.'
  if (chat.capturing) return 'A code snapshot is being captured.'
  if (unsavedFiles) return 'Save or discard the files you are editing first.'
  return ''
}

// MergeButton opens the merge dialog. The dialog is the only place that asks
// for a message: a change under review shows no commit form.
export function MergeButton({ chat, integrationBranch, onMerged }: { chat: Chat; integrationBranch?: string; onMerged: (notice: string) => void }) {
  const id = chat.item.id
  const [open, setOpen] = useState(false)
  const [keepCommits, setKeepCommits] = useState(false)
  const [failure, setFailure] = useState('')
  const message = reviewUI.use(state => state.mergeMessages.get(id) ?? chat.item.title)
  const unsavedFiles = reviewUI.use(state => [...state.drafts.keys()].some(key => key.startsWith(`${id}:`)))
  const mutationKey = ['review', id, 'merge']
  const pending = useIsMutating({ mutationKey }) > 0
  const blocker = mergeBlocker(chat, unsavedFiles)
  const target = integrationBranch || 'the integration branch'
  const merge = useMutation({
    mutationKey,
    mutationFn: (sent: { message: string; keepCommits: boolean }) => itemAction<Outcome>(id, 'merge', { message: sent.keepCommits ? '' : sent.message, rebase: sent.keepCommits }),
    onSuccess: (result, sent) => {
      if (!sent.keepCommits && reviewUI.get().mergeMessages.get(id) === sent.message) setMergeMessage(id)
      setOpen(false)
      onMerged([`Merged into ${target}.`, ...(result.warnings ?? [])].join('\n'))
    },
    onError: (error: Error) => setFailure(error instanceof ApiError && error.outcome ? error.message : 'The merge result could not be confirmed. Check the branch before retrying.'),
  })
  const show = (next: boolean) => {
    setOpen(next)
    if (next) setFailure('')
  }
  return (
    <Dialog open={open} onOpenChange={show}>
      <Button size="sm" variant="outline" disabled={!!blocker || pending} title={blocker || `Merge into ${target}`} onClick={() => show(true)}>
        <GitMergeIcon /> {pending ? 'Merging…' : 'Merge…'}
      </Button>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Merge into {target}</DialogTitle>
          <DialogDescription>
            {keepCommits
              ? 'Wi commits any uncommitted changes, rebases the branch onto the target and lands its commits as they are.'
              : 'Wi commits any uncommitted changes, rebases the branch onto the target and lands it as one commit with this message.'}{' '}
            Nothing is pushed and the item stays open.
          </DialogDescription>
        </DialogHeader>
        {!keepCommits && <Textarea aria-label="Merge message" value={message} rows={4} maxLength={16000} onChange={event => setMergeMessage(id, event.target.value)} className="font-mono text-sm" />}
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={keepCommits} onCheckedChange={setKeepCommits} aria-label="Keep the individual commits" />
          Keep the individual commits
        </label>
        {failure && (
          <p role="alert" className="text-sm whitespace-pre-wrap text-destructive">
            {failure}
          </p>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => show(false)}>
            Cancel
          </Button>
          <Button disabled={!!blocker || pending || (!keepCommits && !message.trim())} title={blocker} onClick={() => merge.mutate({ message, keepCommits })}>
            {pending ? 'Merging…' : 'Merge'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
