import { CodeBlock, CodeBlockActions, CodeBlockContent, CodeBlockCopyButton, CodeBlockLine } from '@/components/ui/code-block'
import { useHighlightedCode } from '@/hooks/use-highlighted-code'
import { cn } from '@/lib/utils'

// Code is a highlighted, copyable block of code or output.
export function Code({
  language,
  value,
  className,
  contentClassName = 'max-h-[32rem]',
  copy = true,
  wrap = false,
  actions,
}: {
  language: string
  value: string
  className?: string
  contentClassName?: string
  copy?: boolean
  wrap?: boolean
  // actions are more buttons beside copying, such as running the code.
  actions?: React.ReactNode
}) {
  const lines = useHighlightedCode(value, language || 'text')
  return (
    <CodeBlock size="sm" className={className}>
      {(copy || actions) && (
        <CodeBlockActions>
          {actions}
          {copy && <CodeBlockCopyButton value={value} />}
        </CodeBlockActions>
      )}
      <CodeBlockContent wrap={wrap} className={cn(contentClassName)}>
        {lines.map((line, index) => (
          <CodeBlockLine key={index}>{line}</CodeBlockLine>
        ))}
      </CodeBlockContent>
    </CodeBlock>
  )
}
