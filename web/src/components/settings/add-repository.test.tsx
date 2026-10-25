import { describe, expect, it } from 'vitest'
import { suggestedName } from './add-repository'

describe('suggestedName', () => {
  it('names a repository after its source', () => {
    expect(suggestedName('git@github.com:regb/workitem.git')).toBe('workitem')
    expect(suggestedName('https://gitlab.com/group/sub/app/')).toBe('app')
    expect(suggestedName('/Users/me/src/my project')).toBe('my-project')
    expect(suggestedName('')).toBe('')
  })
})
