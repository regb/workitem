export { cn } from "cn"

// sentence starts a reason the server gives in lower case as a sentence.
export const sentence = (text: string) => text.charAt(0).toUpperCase() + text.slice(1)
