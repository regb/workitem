import { useContext, useState } from 'react'
import { foldOpen, saveFold } from '@/lib/folds'
import { ItemContext } from './subagent'

// useFold is a fold's open state, remembered for the conversation by key.
// A fold without a stable key (a live tool call not saved yet) is not.
export function useFold(fold: string | undefined) {
  const item = useContext(ItemContext)
  const [open, setOpen] = useState(() => !!fold && !!item && foldOpen(item, fold))
  const change = (next: boolean) => {
    setOpen(next)
    if (fold && item) saveFold(item, fold, next)
  }
  return [open, change] as const
}
