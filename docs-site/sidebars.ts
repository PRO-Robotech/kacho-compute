import type { SidebarsConfig } from '@docusaurus/plugin-content-docs'

const sidebars: SidebarsConfig = {
  computeSidebar: [
    'intro',
    'getting-started',
    {
      type: 'category',
      label: 'Архитектура',
      collapsed: false,
      items: [
        'architecture/overview',
        'architecture/data-model',
        'architecture/instance-lifecycle',
        'architecture/operations',
        'architecture/authz',
      ],
    },
    {
      type: 'category',
      label: 'Установка',
      collapsed: true,
      items: ['install/deploy', 'install/configuration'],
    },
    {
      type: 'category',
      label: 'API',
      collapsed: false,
      items: [
        'api/overview',
        'api/instance',
        'api/disk',
        'api/image',
        'api/snapshot',
        'api/disk-type',
        'api/operations',
      ],
    },
    {
      type: 'category',
      label: 'Дополнительно',
      collapsed: true,
      items: ['advanced/observability', 'advanced/design-decisions'],
    },
  ],
}

export default sidebars
