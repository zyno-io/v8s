import type MarkdownIt from 'markdown-it';

import { defineConfig } from 'vitepress';

const REPO = 'https://github.com/zyno-io/v8s';

/**
 * The design docs link to files outside docs/ (spikes, Go sources) with repo-relative paths. Rewrite those to GitHub URLs at
 * render time so they resolve on the site and the dead-link check stays on for everything else.
 */
function repoLinks(md: MarkdownIt) {
    md.core.ruler.after('inline', 'v8s-repo-links', state => {
        for (const block of state.tokens) {
            for (const tok of block.children ?? []) {
                if (tok.type !== 'link_open') continue;
                const href = tok.attrGet('href');
                const m = href && /^(?:\.\.\/)+((?:spikes|agent|packages|test)\/.*)$/.exec(href);
                if (m) tok.attrSet('href', `${REPO}/blob/main/${m[1]}`);
            }
        }
        return true;
    });
}

export default defineConfig({
    title: 'v8s',
    description: 'A fast, multi-cluster Kubernetes manager for desktop, web, and phone.',
    base: '/v8s/',
    cleanUrls: true,
    lastUpdated: true,
    appearance: true,

    head: [
        ['link', { rel: 'icon', type: 'image/svg+xml', href: '/v8s/logo.svg' }],
        ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
        ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
        ['link', { rel: 'stylesheet', href: 'https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap' }]
    ],

    markdown: {
        config: md => repoLinks(md)
    },

    themeConfig: {
        logo: '/logo.svg',

        nav: [
            { text: 'Guide', link: '/guide/getting-started', activeMatch: '^/guide/' },
            { text: 'Reference', link: '/protocol', activeMatch: '^/protocol' },
            { text: 'Design', link: '/architecture' },
            { text: 'GitHub', link: REPO }
        ],

        sidebar: [
            {
                text: 'Guide',
                items: [
                    { text: 'Getting started', link: '/guide/getting-started' },
                    { text: 'Install', link: '/guide/install' },
                    { text: 'Clusters', link: '/guide/clusters' },
                    { text: 'Navigation', link: '/guide/navigation' },
                    { text: 'Problems', link: '/guide/problems' },
                    { text: 'Flux, Helm, and integrations', link: '/guide/integrations' },
                    { text: 'Operations', link: '/guide/operations' },
                    { text: 'Hosted mode', link: '/guide/hosted' },
                    { text: 'FAQ', link: '/guide/faq' }
                ]
            },
            {
                text: 'Reference',
                items: [{ text: 'Agent-UI protocol', link: '/protocol' }]
            },
            {
                text: 'Design',
                items: [
                    { text: 'Architecture and plan', link: '/architecture' },
                    { text: 'Performance', link: '/performance' },
                    { text: 'Data model', link: '/data-model' },
                    { text: 'Problems engine', link: '/problems' },
                    { text: 'Integrations', link: '/integrations' },
                    { text: 'Operations and security', link: '/operations' },
                    { text: 'Authentication', link: '/auth' },
                    { text: 'Packaging', link: '/packaging' },
                    { text: 'Design system', link: '/design-system' }
                ]
            }
        ],

        socialLinks: [{ icon: 'github', link: REPO }],
        search: { provider: 'local' },
        editLink: { pattern: `${REPO}/edit/main/docs/:path`, text: 'Edit this page on GitHub' },
        outline: [2, 3],
        footer: { message: 'Released under the MIT License.', copyright: 'Copyright © Signal24' }
    }
});
