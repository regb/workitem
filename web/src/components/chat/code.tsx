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
}: {
  language: string
  value: string
  className?: string
  contentClassName?: string
  copy?: boolean
  wrap?: boolean
}) {
  const lines = useHighlightedCode(value, language || 'text')
  return (
    <CodeBlock size="sm" className={className}>
      {copy && (
        <CodeBlockActions>
          <CodeBlockCopyButton value={value} />
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
