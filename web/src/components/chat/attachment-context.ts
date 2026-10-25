import { createContext, useContext } from 'react'

// AttachmentLinks are the attachments Markdown may link to, by name, and
// how to open one. Without them, Markdown links none.
export type AttachmentLinks = { names: string[]; open: (name: string) => void }

export const AttachmentLinksContext = createContext<AttachmentLinks>({ names: [], open: () => {} })
export const useAttachmentLinks = () => useContext(AttachmentLinksContext)
