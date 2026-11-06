// Drafts are what was typed or picked but not sent yet. They survive leaving
// a conversation or closing the create box within the tab, never a reload.
// They are not reactive: a field reads its draft when it mounts.
export const drafts = {
  // messages holds each conversation's unsent message, by item.
  messages: new Map<string, string>(),
  // task is the new work being written in the create box.
  task: '',
  // agent is the profile last picked for new work.
  agent: '',
  // choices holds the model and thinking level last picked for new work, by
  // profile; empty means the profile's own.
  choices: new Map<string, { model: string; level: string }>(),
}

// saveMessage keeps a conversation's unsent message; an empty one is dropped.
export function saveMessage(id: string, text: string) {
  if (text) drafts.messages.set(id, text)
  else drafts.messages.delete(id)
}
