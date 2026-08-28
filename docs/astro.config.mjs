import sitemap from '@astrojs/sitemap';
import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';

const repository = 'https://github.com/regb/workitem';

export default defineConfig({
  site: 'https://regb.github.io',
  base: process.env.NODE_ENV === 'development' ? '/' : '/workitem',
  devToolbar: { enabled: false },
  integrations: [
    starlight({
      title: 'wi',
      description: 'Durable work items for AI-assisted development.',
      disable404Route: true,
      social: [{ icon: 'github', label: 'GitHub', href: repository }],
      editLink: { baseUrl: `${repository}/edit/main/docs/` },
      customCss: ['./src/styles/custom.css'],
      sidebar: [
        { label: 'Overview', slug: 'index' },
        {
          label: 'Getting started',
          items: [
            { label: 'Install wi', slug: 'getting-started/installation' },
            { label: 'Quick start', slug: 'getting-started/quick-start' },
          ],
        },
        {
          label: 'Core concepts',
          items: [
            { label: 'Work items', slug: 'concepts/work-items' },
            { label: 'Lifecycle', slug: 'concepts/lifecycle' },
            { label: 'Workspaces', slug: 'concepts/workspaces' },
            { label: 'Agents and conversations', slug: 'concepts/agents' },
            { label: 'Attention', slug: 'concepts/attention' },
          ],
        },
        {
          label: 'Guides',
          items: [
            { label: 'Everyday workflow', slug: 'guides/everyday-workflow' },
            { label: 'Delegate work', slug: 'guides/delegation' },
            { label: 'Review agent work', slug: 'guides/review-and-follow-up' },
            { label: 'Use tmux', slug: 'guides/tmux' },
            { label: 'Merge completed work', slug: 'guides/merging' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Command map', slug: 'reference/commands' },
            { label: 'Configuration', slug: 'reference/configuration' },
            { label: 'Data and diagnostics', slug: 'reference/data-and-diagnostics' },
            { label: 'Troubleshooting', slug: 'reference/troubleshooting' },
          ],
        },
        { label: 'Philosophy', slug: 'philosophy/philosophy' },
      ],
    }),
    sitemap(),
  ],
});
