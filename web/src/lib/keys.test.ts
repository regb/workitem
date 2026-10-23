import type { KeyboardEvent } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { submitsOnEnter } from './keys'

const press = (key: string, shiftKey = false) => ({ key, shiftKey, keyCode: 13, nativeEvent: { isComposing: false } }) as unknown as KeyboardEvent<HTMLTextAreaElement>
const device = (touch: boolean) => vi.stubGlobal('matchMedia', (query: string) => ({ matches: touch && query.includes('pointer: coarse') }))

describe('submitsOnEnter', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('sends on Enter with a keyboard, and adds a line with Shift', () => {
    device(false)
    expect(submitsOnEnter(press('Enter'))).toBe(true)
    expect(submitsOnEnter(press('Enter', true))).toBe(false)
  })

  it('adds a line on a touch keyboard', () => {
    device(true)
    expect(submitsOnEnter(press('Enter'))).toBe(false)
  })
})
