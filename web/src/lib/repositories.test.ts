import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { describe, expect, it, vi } from 'vitest'
import { SetupTrust } from '@/gen/wi/core/v1/repositories_pb'

const calls: Array<{ method: string; request: unknown }> = []
const answers: Record<string, unknown> = {
  listRepositories: { repositories: [{ id: 'r1', name: 'workitem', integrationBranch: 'main', createdAt: timestampFromDate(new Date('2026-11-01T00:00:00Z')), upstream: { url: 'https://github.com/regb/workitem.git', branch: 'main', webUrl: 'https://github.com/regb/workitem/tree/main', host: 'github' } }] },
  listRepositoryFiles: { files: [{ path: '.env', size: 12, sha256: 'abc', modifiedAt: timestampFromDate(new Date('2026-11-02T00:00:00Z')) }] },
  getCheckoutSetup: { revision: 4, setup: { run: 'make', trust: SetupTrust.ALWAYS, timeoutSeconds: 600, skip: ['.env'], declared: '' } },
  getEnvironment: { revision: 2, variables: [{ key: 'A', value: '1' }], staleAgents: [] },
  getRepository: { repository: { id: 'r1', name: 'workitem' } },
  updateEnvironment: {},
}
vi.mock('./rpc', async original => ({
  ...(await original<object>()),
  repositories: new Proxy(
    {},
    {
      get: (_, method: string) => async (request: unknown) => {
        calls.push({ method, request })
        return answers[method]
      },
    },
  ),
}))

describe('repository view models', () => {
  it('build the settings pages from the API', async () => {
    const { listRepositories, readSetup, saveEnvironment } = await import('./repositories')
    expect(await listRepositories()).toEqual([
      {
        id: 'r1',
        name: 'workitem',
        integration_branch: 'main',
        created_at: '2026-11-01T00:00:00.000Z',
        remote: { url: 'https://github.com/regb/workitem.git', branch: 'main', web_url: 'https://github.com/regb/workitem/tree/main', host: 'github' },
        files: [{ path: '.env', size: 12, sha256: 'abc', updated_at: '2026-11-02T00:00:00.000Z' }],
      },
    ])
    expect(await readSetup('r1')).toEqual({ revision: 4, run: 'make', trust: 'always', timeout_seconds: 600, skip: ['.env'], declared: undefined })
    const saved = await saveEnvironment('r1', [{ key: 'A', value: '2' }], 2)
    expect(calls.find(call => call.method === 'updateEnvironment')?.request).toEqual({ repositoryId: 'r1', expectedRevision: 2, replace: true, set: [{ key: 'A', value: '2' }] })
    expect(saved.project).toEqual({ id: 'r1', name: 'workitem' })
  })
})
