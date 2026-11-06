import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentStatus, ItemState } from '@/gen/wi/core/v1/common_pb'
import { ItemObservationSchema, ItemSummarySchema, ListSection } from '@/gen/wi/core/v1/items_pb'
import { workListView } from './items'

const row = (id: string, section: ListSection, agentStatus = AgentStatus.UNSPECIFIED) =>
  create(ItemSummarySchema, { id, slug: id, title: id, state: ItemState.ACTIVE, section, repositoryId: 'repo', observation: create(ItemObservationSchema, { agentStatus, unread: true }) })

describe('workListView', () => {
  it('groups rows under the daemon’s sections, in their order, keeping the order of rows', () => {
    const list = workListView(
      [row('b', ListSection.IN_PROGRESS, AgentStatus.BUSY), row('a', ListSection.NEEDS_ATTENTION), row('c', ListSection.NEEDS_FIXING, AgentStatus.PROBLEM), row('d', ListSection.IN_PROGRESS), row('e', ListSection.CLOSED)],
      [{ id: 'repo', name: 'Repo' }],
    )
    expect(list.display_sections.map(section => [section.name, section.items.map(item => item.id)])).toEqual([
      ['NEEDS ATTENTION', ['a']],
      ['NEEDS FIXING', ['c']],
      ['IN PROGRESS', ['b', 'd']],
      ['CLOSED', ['e']],
    ])
    expect(list.display_sections[1].items[0]).toMatchObject({ state: 'active', agent: { status: 'problem' }, unread: true, project_id: 'repo' })
    expect(list.projects).toEqual([{ id: 'repo', name: 'Repo' }])
  })
})
