import { createRouter, createWebHistory } from 'vue-router';

import { isPhone } from './lib/viewport.ts';

export default createRouter({
    history: createWebHistory(import.meta.env.BASE_URL),
    routes: [
        // Problems is the landing tab on a phone, pods on a desktop.
        { path: '/', redirect: () => (isPhone.value ? '/problems' : '/pods') },
        { path: '/pods', name: 'pods', component: () => import('./pages/ResourcePage.vue'), props: { gvr: 'v1/pods' } },
        {
            path: '/r/:gvr(.*)',
            name: 'resource',
            component: () => import('./pages/ResourcePage.vue'),
            props: route => ({ gvr: String(Array.isArray(route.params.gvr) ? route.params.gvr.join('/') : route.params.gvr) })
        },
        { path: '/flux', name: 'flux', component: () => import('./pages/FluxOverviewPage.vue') },
        { path: '/flux/graph', name: 'flux-graph', component: () => import('./pages/FluxGraphPage.vue') },
        { path: '/problems', name: 'problems', component: () => import('./pages/ProblemsPage.vue') },
        { path: '/overview', name: 'overview', component: () => import('./pages/OverviewPage.vue') },
        // Security: `/security` is the Vulnerabilities overview (an image's reports open through `?image=`), `/security/policies` the Kyverno policies.
        { path: '/security', name: 'security', component: () => import('./pages/SecurityPage.vue') },
        { path: '/security/policies', name: 'security-policies', component: () => import('./pages/SecurityPage.vue') },
        // Network (Hubble): drops, service map, relay status.
        { path: '/network/drops', name: 'network-drops', component: () => import('./pages/NetworkPage.vue') },
        { path: '/network/map', name: 'network-map', component: () => import('./pages/NetworkPage.vue') },
        { path: '/network/status', name: 'network-status', component: () => import('./pages/NetworkPage.vue') },
        { path: '/:rest(.*)*', redirect: '/pods' }
    ]
});
