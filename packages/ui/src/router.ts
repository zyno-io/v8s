import { createRouter, createWebHistory } from 'vue-router';

export default createRouter({
    history: createWebHistory(import.meta.env.BASE_URL),
    routes: [
        { path: '/', redirect: '/pods' },
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
        { path: '/overview', name: 'overview', component: () => import('./pages/ProblemsPage.vue'), meta: { placeholder: 'Overview' } },
        { path: '/:rest(.*)*', redirect: '/pods' }
    ]
});
