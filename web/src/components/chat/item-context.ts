import { createContext } from 'react'

// Every nested message, including subagent output, belongs to this item and
// resolves files against its current workspace, never the browser's filesystem.
export const ItemContext = createContext('')
