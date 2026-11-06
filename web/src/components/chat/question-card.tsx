import { useState } from 'react'
import { CheckIcon, MessageCircleQuestionIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { answerQuestion } from '@/lib/actions'
import type { HistoryTool } from '@/lib/api'
import { answerOf, parseQuestions, type Choice, type Question } from '@/lib/questions'
import { cn } from '@/lib/utils'

// QuestionCard shows the question the agent waits on, with its options, and
// answers it as the agent's own dialog would. Each question takes one option,
// several when it allows them, or the operator's own text.
export function QuestionCard({ id, question, readOnly = false }: { id: string; question: HistoryTool; readOnly?: boolean }) {
  const questions = parseQuestions(question.input)
  const [choices, setChoices] = useState<Record<string, Choice>>({})
  const [sending, setSending] = useState(false)
  if (!questions.length) return null
  const choose = (q: Question, update: (choice: Choice) => Choice) => setChoices(current => ({ ...current, [q.question]: update(current[q.question] ?? { labels: [], other: '' }) }))
  const complete = questions.every(q => answerOf(choices[q.question]))
  // Another client controls the agent: answering needs control, like sending.
  const locked = sending || readOnly
  const submit = async () => {
    setSending(true)
    const answers = Object.fromEntries(questions.map(q => [q.question, answerOf(choices[q.question])]))
    await answerQuestion(id, question.id ?? '', answers)
    setSending(false)
  }
  return (
    <section aria-label="The agent's question" className="flex flex-col gap-4 rounded-lg border bg-muted/30 p-4 text-sm">
      <p className="flex items-center gap-2 font-medium">
        <MessageCircleQuestionIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        The agent is waiting for your answer
      </p>
      {questions.map(q => {
        const choice = choices[q.question]
        return (
          <fieldset key={q.question} className="flex flex-col gap-2">
            <legend className="mb-2 flex flex-wrap items-baseline gap-2">
              {q.header && <span className="rounded-sm bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">{q.header}</span>}
              <span className="font-medium">{q.question}</span>
              {q.multiSelect && <span className="text-xs text-muted-foreground">Choose any</span>}
            </legend>
            <div role={q.multiSelect ? 'group' : 'radiogroup'} aria-label={q.question} className="flex flex-col gap-1.5">
              {q.options.map(option => {
                const picked = !!choice?.labels.includes(option.label)
                return (
                  <button
                    key={option.label}
                    type="button"
                    role={q.multiSelect ? 'checkbox' : 'radio'}
                    aria-checked={picked}
                    disabled={locked}
                    onClick={() =>
                      choose(q, current =>
                        q.multiSelect
                          ? { ...current, labels: picked ? current.labels.filter(label => label !== option.label) : [...current.labels, option.label] }
                          : // One option, or the operator's own text, not both.
                            { labels: [option.label], other: '' },
                      )
                    }
                    className={cn('flex items-start gap-2 rounded-md border bg-background px-3 py-2 text-left transition-colors hover:bg-accent', picked && 'border-primary bg-accent')}
                  >
                    <span
                      className={cn(
                        'mt-0.5 flex size-4 shrink-0 items-center justify-center border',
                        q.multiSelect ? 'rounded-[4px]' : 'rounded-full',
                        picked && 'border-primary bg-primary text-primary-foreground',
                      )}
                      aria-hidden
                    >
                      {picked && <CheckIcon className="size-3" />}
                    </span>
                    <span className="flex flex-col">
                      <span>{option.label}</span>
                      {option.description && <span className="text-xs text-muted-foreground">{option.description}</span>}
                    </span>
                  </button>
                )
              })}
              <Input
                aria-label={`Your own answer to: ${q.question}`}
                placeholder="Or write your own answer"
                maxLength={4000}
                disabled={locked}
                value={choice?.other ?? ''}
                onChange={change => {
                  const other = change.target.value
                  choose(q, current => (q.multiSelect ? { ...current, other } : { labels: other.trim() ? [] : current.labels, other }))
                }}
                onKeyDown={key => key.key === 'Enter' && complete && !locked && void submit()}
              />
            </div>
          </fieldset>
        )
      })}
      {readOnly && <p className="text-xs text-muted-foreground">Take control to answer here.</p>}
      <Button className="self-end" size="sm" disabled={!complete || locked} onClick={() => void submit()}>
        {sending ? 'Answering…' : 'Answer'}
      </Button>
    </section>
  )
}
