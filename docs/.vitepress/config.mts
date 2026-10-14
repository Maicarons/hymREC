import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'hymREC',
  description: '面向虚拟主播直播切片的关键词触发录制系统',
  lang: 'zh-CN',
  // 文档站部署于 GitHub Pages 项目站点：https://maicarons.github.io/hymREC/
  base: '/hymREC/',
  cleanUrls: true,
  lastUpdated: true,
  head: [
    ['meta', { name: 'theme-color', content: '#fb7299' }],
  ],
  themeConfig: {
    logo: '/logo.svg',
    nav: [
      { text: '首页', link: '/' },
      { text: '指南', link: '/guide/quickstart' },
      { text: 'GitHub', link: 'https://github.com/Maicarons/hymREC' },
    ],
    sidebar: [
      {
        text: '指南',
        items: [
          { text: '简介', link: '/guide/intro' },
          { text: '快速开始', link: '/guide/quickstart' },
          { text: '系统架构', link: '/guide/architecture' },
          { text: '关键词策略', link: '/guide/keywords' },
          { text: '配置说明', link: '/guide/configuration' },
          { text: 'ASR 接入', link: '/guide/asr' },
          { text: '弹幕辅助触发', link: '/guide/danmaku' },
          { text: '参数调优', link: '/guide/tuning' },
          { text: '常见问题', link: '/guide/faq' },
          { text: '法律声明', link: '/guide/legal' },
        ],
      },
      {
        text: '资源',
        items: [
          { text: '论文', link: '/paper' },
        ],
      },
      {
        text: '开发',
        items: [
          { text: '项目结构', link: '/dev/structure' },
          { text: '测试', link: '/dev/testing' },
          { text: '构建与发布', link: '/dev/build' },
        ],
      },
    ],
    footer: {
      message: 'AGPL-3.0 License · 仅供学习交流',
      copyright: 'Copyright © 2026 hymREC contributors',
    },
    editLink: {
      pattern: 'https://github.com/Maicarons/hymREC/edit/main/docs/:path',
    },
    socialLinks: [
      { icon: 'github', link: 'https://github.com/Maicarons/hymREC' },
    ],
  },
})
