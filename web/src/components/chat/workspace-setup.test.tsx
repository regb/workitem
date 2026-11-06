import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { WorkspaceSetupState } from './workspace-setup'

describe('WorkspaceSetupState', () => {
  it('shows setup in progress with the end of its log', () => {
    const { container } = render(<WorkspaceSetupState id="i" setup={{ current: true, run: { status: 'running' }, log_tail: 'copied .env\n$ npm install\n' }} />)
    expect(container.textContent).toContain('Setting up the workspace')
    expect(container.querySelector('pre')?.textContent).toBe('copied .env\n$ npm install')
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('explains a failed setup and offers to retry it', () => {
    const { container } = render(<WorkspaceSetupState id="i" setup={{ current: true, run: { status: 'failed', error: 'setup command failed: exit status 1' } }} />)
    expect(container.textContent).toContain('the agent did not start')
    expect(container.textContent).toContain('exit status 1')
    expect(screen.getByRole('button', { name: 'Retry setup' })).toBeTruthy()
  })
})
