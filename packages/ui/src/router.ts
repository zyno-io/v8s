import { createRouter, createWebHistory } from 'vue-router';

import { KEY_ROUTE, readJson, restoreRoute, routeToRemember, writeJson } from './lib/remember.ts';
import { isPhone } from './lib/viewport.ts';

const router = createRouter({
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

// Opening the app at `/` lands on the last visited page. Only the app's first navigation does this, so going to `/` later
// (Pods, or Problems on a phone) works as before. The page is remembered as path + query, minus the drawer's object.
let first = true;
router.beforeEach(to => {
    const wasFirst = first;
    first = false;
    // `/` is a redirect record, so by the time a guard runs `to` is already Pods: look at where the navigation started.
    const entry = to.redirectedFrom ?? to;
    const dest = restoreRoute(wasFirst, entry.path, entry.query, readJson(KEY_ROUTE));
    if (!dest) return true;
    const r = router.resolve(dest);
    return { path: r.path, query: r.query, hash: to.hash };
});
router.afterEach((to, _from, failure) => {
    if (failure) return;
    const r = routeToRemember(to.path, to.query);
    if (r) writeJson(KEY_ROUTE, r);
});

export default router;
