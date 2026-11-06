// Questions an agent asks, from its question tool call's input (Claude's
// AskUserQuestion): each question with its options.
export type QuestionOption = { label: string; description?: string }
export type Question = { question: string; header?: string; multiSelect?: boolean; options: QuestionOption[] }

// parseQuestions reads a question call's input, or returns nothing when it
// holds no question.
export function parseQuestions(input?: string): Question[] {
  if (!input) return []
  try {
    const value = JSON.parse(input) as { questions?: unknown }
    if (!Array.isArray(value.questions)) return []
    return value.questions.flatMap(raw => {
      const q = raw as Partial<Question>
      if (typeof q?.question !== 'string' || !q.question) return []
      const options = Array.isArray(q.options) ? q.options.filter(option => typeof option?.label === 'string' && option.label) : []
      return [{ question: q.question, header: typeof q.header === 'string' ? q.header : undefined, multiSelect: !!q.multiSelect, options }]
    })
  } catch {
    return []
  }
}

// Choice is what the operator picked for one question: option labels, and
// their own text when they wrote one.
export type Choice = { labels: string[]; other: string }

// answerOf turns a choice into the agent's answer: the label, labels joined by
// ", " for a multi-select question, with the operator's own text last.
export function answerOf(choice: Choice | undefined) {
  if (!choice) return ''
  const parts = [...choice.labels]
  if (choice.other.trim()) parts.push(choice.other.trim())
  return parts.join(', ')
}
