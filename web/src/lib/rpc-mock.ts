// A stand-in for the API clients in tests. Mock the module with
//
//   vi.mock('@/lib/rpc', async original => (await import('@/lib/rpc-mock')).mockRPC(await original()))
//
// Every call is recorded in rpcCalls as service.method and its request; a
// reply set in rpcReplies answers it (it may throw), else it answers {}.
// Jobs finish at once: awaitJob resolves with the job it is given.

export type Call = { call: string; request: Record<string, unknown> }
export const rpcCalls: Call[] = []
export const rpcReplies = new Map<string, (request: Record<string, unknown>) => unknown>()

export function resetRPC() {
  rpcCalls.length = 0
  rpcReplies.clear()
}

// calls lists the requests made to one method, as service.method.
export const calls = (call: string) => rpcCalls.filter(c => c.call === call).map(c => c.request)

function client(service: string) {
  return new Proxy(
    {},
    {
      get: (_, method: string) => async (request: Record<string, unknown>) => {
        const call = `${service}.${method}`
        rpcCalls.push({ call, request })
        const reply = rpcReplies.get(call)
        return reply ? reply(request) : {}
      },
    },
  )
}

export function mockRPC(original: object) {
  return {
    ...original,
    items: client('items'),
    agents: client('agents'),
    control: client('control'),
    lifecycle: client('lifecycle'),
    workspaces: client('workspaces'),
    jobs: client('jobs'),
    profiles: client('profiles'),
    awaitJob: async (job: unknown) => job,
  }
}
