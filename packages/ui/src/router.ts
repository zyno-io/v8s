import { createRouter, createWebHistory } from 'vue-router';

export default createRouter({
    history: createWebHistory(import.meta.env.BASE_URL === './' ? undefined : import.meta.env.BASE_URL),
    routes: [
        { path: '/', redirect: '/pods' },
        { path: '/pods', name: 'pods', component: () => import('./pages/PodsPage.vue') },
        { path: '/problems', name: 'problems', component: () => import('./pages/ProblemsPage.vue') },
        { path: '/:rest(.*)*', redirect: '/pods' }
    ]
});
