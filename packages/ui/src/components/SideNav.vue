<script setup lang="ts">
import { computed, nextTick, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import { getPodsAll, getProblemsAll, kinds } from '@/lib/agent.ts';
import { formatCount } from '@/lib/format.ts';
import { buildSidebar } from '@/lib/kinds.ts';
import { gvrPath, PODS_GVR } from '@/lib/route.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const settings = useSettingsStore();
const route = useRoute();
const router = useRouter();
const ui = useUiStore();
const podsAll = getPodsAll();
const problemsAll = getProblemsAll();
const integrations = useIntegrationsStore();
const problemCount = computed(() => problemsAll.total.value);
// Trivy report kinds that get a generic table under Security (config audit, exposed secrets, RBAC/infra assessments).
const TRIVY_REPORTS: [string, string][] = [
    ['aquasecurity.github.io/v1alpha1/configauditreports', 'Config audits'],
    ['aquasecurity.github.io/v1alpha1/exposedsecretreports', 'Exposed secrets'],
    ['aquasecurity.github.io/v1alpha1/rbacassessmentreports', 'RBAC assessments'],
    ['aquasecurity.github.io/v1alpha1/infraassessmentreports', 'Infra assessments']
];
const trivyReports = computed(() => TRIVY_REPORTS.filter(([gvr]) => kinds.value.some(k => k.gvr === gvr)));

// Counts only where a subscription already exists (pods); opening watches just for sidebar numbers is a non-goal.
const podCount = computed(() => (podsAll.total.value ? formatCount(podsAll.total.value) : ''));
const countFor = (gvr: string) => (gvr === PODS_GVR ? podCount.value : '');

const model = computed(() => buildSidebar(kinds.value, settings.stars));

// ---- collapse state (per-viewer convenience, remembered in localStorage) ----
function loadCollapsed(): Set<string> {
    try {
        const raw = localStorage.getItem('v8s:nav-collapsed');
        if (raw) return new Set(JSON.parse(raw) as string[]);
    } catch {
        // storage unavailable
    }
    return new Set(['crd']);
}
const collapsed = ref<Set<string>>(loadCollapsed());
// A group is collapsed by default for custom resources; track explicit opens.
const opened = ref<Set<string>>(new Set());
function toggleSection(id: string) {
    const n = new Set(collapsed.value);
    if (!n.delete(id)) n.add(id);
    collapsed.value = n;
    try {
        localStorage.setItem('v8s:nav-collapsed', JSON.stringify([...n]));
    } catch {
        // ignore
    }
}
function toggleGroup(g: string) {
    const n = new Set(opened.value);
    if (!n.delete(g)) n.add(g);
    opened.value = n;
}

const activeGvr = computed(() => (route.path === '/pods' ? PODS_GVR : route.path.startsWith('/r/') ? route.path.slice(3) : ''));

function toggleStar(gvr: string) {
    void settings.toggleStarred(gvr).catch(e => ui.toast('danger', e instanceof Error ? e.message : String(e)));
}

// ---- bookmarks ----
const adding = ref(false);
const bmName = ref('');
const bmInput = ref<HTMLInputElement | null>(null);

async function startBookmark() {
    adding.value = true;
    bmName.value = document.title === 'v8s' ? route.path.replace(/^\/(r\/)?/, '') : document.title;
    const gvr = activeGvr.value;
    if (gvr)
        bmName.value =
            (kinds.value.find(k => k.gvr === gvr)?.plural ?? gvr.split('/').pop() ?? gvr) + (route.query.n ? ` · ${String(route.query.n)}` : '');
    await nextTick();
    bmInput.value?.select();
}
async function saveBookmark() {
    const name = bmName.value.trim();
    if (!name) return;
    adding.value = false;
    try {
        const url = route.fullPath;
        await settings.patch(d => ({ bookmarks: [...d.bookmarks, { name, url }] }));
    } catch (e) {
        ui.toast('danger', e instanceof Error ? e.message : String(e));
    }
}
function removeBookmark(i: number) {
    void settings.patch({ bookmarks: settings.bookmarks.filter((_, k) => k !== i) }).catch(() => {});
}
</script>

<template>
    <nav class="side" aria-label="Resources">
        <div class="navsec">Starred</div>
        <div v-for="it in model.starred" :key="it.gvr" class="navrow">
            <router-link class="nav" :to="gvrPath(it.gvr)">{{ it.label }}<span class="n">{{ countFor(it.gvr) }}</span></router-link>
            <button class="star on" type="button" :aria-label="`Unstar ${it.label}`" @click="toggleStar(it.gvr)">
                <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
                    <path d="M8 1.8l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.4 4.1 13.6 5 9.3l-3.2-3 4.3-.5z" />
                </svg>
            </button>
        </div>
        <p v-if="!model.starred.length" class="hint">Star a type from its table header.</p>

        <div class="navsec">Cluster</div>
        <router-link class="nav" to="/overview">Overview</router-link>
        <router-link class="nav" to="/problems" data-testid="nav-problems"
            >Problems<span v-if="problemCount" class="tag danger pbadge" data-testid="nav-problems-count">{{
                formatCount(problemCount)
            }}</span></router-link
        >
        <template v-for="s in model.sections" :key="'c-' + s.id">
            <template v-if="s.id === 'cluster'">
                <div v-for="it in s.items" :key="it.to ?? it.gvr" class="navrow">
                    <router-link class="nav" :class="{ active: it.to ? route.path === it.to : activeGvr === it.gvr }" :to="it.to ?? gvrPath(it.gvr)"
                        >{{ it.label }}<span class="n">{{ countFor(it.gvr) }}</span></router-link
                    >
                    <button
                        v-if="!it.to"
                        class="star"
                        :class="{ on: settings.isStarred(it.gvr) }"
                        type="button"
                        :aria-pressed="settings.isStarred(it.gvr)"
                        :aria-label="settings.isStarred(it.gvr) ? `Unstar ${it.label}` : `Star ${it.label}`"
                        @click="toggleStar(it.gvr)"
                    >
                        <svg
                            width="12"
                            height="12"
                            viewBox="0 0 16 16"
                            :fill="settings.isStarred(it.gvr) ? 'currentColor' : 'none'"
                            stroke="currentColor"
                            stroke-width="1.5"
                            aria-hidden="true"
                        >
                            <path d="M8 1.8l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.4 4.1 13.6 5 9.3l-3.2-3 4.3-.5z" />
                        </svg>
                    </button>
                </div>
            </template>
        </template>

        <template v-if="integrations.detected.security">
            <div class="navsec">Security</div>
            <router-link class="nav" to="/security">Vulnerabilities</router-link>
            <router-link v-if="integrations.detected.kyverno" class="nav" to="/security/policies">Policies</router-link>
            <router-link v-for="[gvr, label] in trivyReports" :key="gvr" class="nav" :to="gvrPath(gvr)">{{ label }}</router-link>
        </template>
        <template v-if="integrations.hubble">
            <div class="navsec">Network flows</div>
            <router-link class="nav" to="/network/drops">Drops</router-link>
            <router-link v-if="!isPhone" class="nav" to="/network/map">Service map</router-link>
            <router-link class="nav" to="/network/status">Hubble status</router-link>
        </template>

        <template v-for="s in model.sections.filter(x => x.id !== 'cluster')" :key="s.id">
            <div class="navsec">
                <button class="sechead" type="button" :aria-expanded="!collapsed.has(s.id)" @click="toggleSection(s.id)">
                    <svg
                        class="chev"
                        :class="{ open: !collapsed.has(s.id) }"
                        width="9"
                        height="9"
                        viewBox="0 0 16 16"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        aria-hidden="true"
                    >
                        <path d="M6 3l5 5-5 5" /></svg
                    >{{ s.title }}
                </button>
            </div>
            <template v-if="!collapsed.has(s.id)">
                <div v-for="it in s.items" :key="it.to ?? it.gvr" class="navrow">
                    <router-link class="nav" :class="{ active: it.to ? route.path === it.to : activeGvr === it.gvr }" :to="it.to ?? gvrPath(it.gvr)"
                        >{{ it.label }}<span class="n">{{ countFor(it.gvr) }}</span></router-link
                    >
                    <button
                        v-if="!it.to"
                        class="star"
                        :class="{ on: settings.isStarred(it.gvr) }"
                        type="button"
                        :aria-pressed="settings.isStarred(it.gvr)"
                        :aria-label="settings.isStarred(it.gvr) ? `Unstar ${it.label}` : `Star ${it.label}`"
                        @click="toggleStar(it.gvr)"
                    >
                        <svg
                            width="12"
                            height="12"
                            viewBox="0 0 16 16"
                            :fill="settings.isStarred(it.gvr) ? 'currentColor' : 'none'"
                            stroke="currentColor"
                            stroke-width="1.5"
                            aria-hidden="true"
                        >
                            <path d="M8 1.8l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.4 4.1 13.6 5 9.3l-3.2-3 4.3-.5z" />
                        </svg>
                    </button>
                </div>
            </template>
        </template>

        <template v-if="model.crds.length">
            <div class="navsec">
                <button class="sechead" type="button" :aria-expanded="!collapsed.has('crd')" @click="toggleSection('crd')">
                    <svg
                        class="chev"
                        :class="{ open: !collapsed.has('crd') }"
                        width="9"
                        height="9"
                        viewBox="0 0 16 16"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        aria-hidden="true"
                    >
                        <path d="M6 3l5 5-5 5" /></svg
                    >Custom resources
                </button>
            </div>
            <template v-if="!collapsed.has('crd')">
                <div v-for="g in model.crds" :key="g.group" class="grp">
                    <button class="nav grouphead" type="button" :aria-expanded="opened.has(g.group)" @click="toggleGroup(g.group)">
                        <svg
                            class="chev"
                            :class="{ open: opened.has(g.group) }"
                            width="9"
                            height="9"
                            viewBox="0 0 16 16"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="2"
                            aria-hidden="true"
                        >
                            <path d="M6 3l5 5-5 5" />
                        </svg>
                        <span class="gname">{{ g.group }}</span><span class="n">{{ g.items.length }}</span>
                    </button>
                    <template v-if="opened.has(g.group)">
                        <div v-for="it in g.items" :key="it.gvr" class="navrow sub">
                            <router-link class="nav" :class="{ active: activeGvr === it.gvr }" :to="gvrPath(it.gvr)">{{ it.label }}</router-link>
                            <button
                                class="star"
                                :class="{ on: settings.isStarred(it.gvr) }"
                                type="button"
                                :aria-pressed="settings.isStarred(it.gvr)"
                                :aria-label="settings.isStarred(it.gvr) ? `Unstar ${it.label}` : `Star ${it.label}`"
                                @click="toggleStar(it.gvr)"
                            >
                                <svg
                                    width="12"
                                    height="12"
                                    viewBox="0 0 16 16"
                                    :fill="settings.isStarred(it.gvr) ? 'currentColor' : 'none'"
                                    stroke="currentColor"
                                    stroke-width="1.5"
                                    aria-hidden="true"
                                >
                                    <path d="M8 1.8l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.4 4.1 13.6 5 9.3l-3.2-3 4.3-.5z" />
                                </svg>
                            </button>
                        </div>
                    </template>
                </div>
            </template>
        </template>

        <div class="navsec bmhead">
            <span>Bookmarks</span>
            <button
                class="plus"
                type="button"
                aria-label="Bookmark this view"
                title="Bookmark this view"
                data-testid="bookmark-add"
                @click="startBookmark"
            >
                +
            </button>
        </div>
        <form v-if="adding" class="bmform" @submit.prevent="saveBookmark" @keydown.esc.stop="adding = false">
            <label class="field"><input ref="bmInput" v-model="bmName" type="text" aria-label="Bookmark name" placeholder="Name" /></label>
            <button class="btn sm primary" type="submit">Save</button>
        </form>
        <div v-for="(b, i) in settings.bookmarks" :key="b.url + i" class="navrow">
            <a class="nav" :href="b.url" @click.prevent="router.push(b.url)">{{ b.name }}</a>
            <button class="star on x" type="button" :aria-label="`Remove bookmark ${b.name}`" @click="removeBookmark(i)">×</button>
        </div>
        <p v-if="!settings.bookmarks.length && !adding" class="hint">Bookmark a view, with its filters and open object.</p>
    </nav>
</template>

<style scoped>
.navrow {
    position: relative;
    display: flex;
    align-items: center;
}
.navrow .nav {
    flex-grow: 1;
    min-width: 0;
    padding-right: 28px;
}
.navrow.sub .nav {
    padding-left: 26px;
}
.nav :deep(.n),
.n {
    margin-left: auto;
}
.soon {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
}
.star {
    all: unset;
    box-sizing: border-box;
    position: absolute;
    right: 6px;
    width: 20px;
    height: 20px;
    display: grid;
    place-items: center;
    border-radius: 4px;
    color: var(--text3);
    cursor: pointer;
    opacity: 0;
}
.navrow:hover .star,
.star:focus-visible,
.star.on {
    opacity: 1;
}
.star.on {
    color: var(--warning);
}
.star.x {
    color: var(--text3);
    font-size: 15px;
    opacity: 0;
}
.navrow:hover .star.x,
.star.x:focus-visible {
    opacity: 1;
}
.star:focus-visible {
    outline: 2px solid var(--focus);
}
.hint {
    margin: 2px 10px 4px;
    font-size: 12px;
    color: var(--text3);
}
.sechead,
.grouphead {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    width: 100%;
    border-radius: 4px;
}
.grouphead {
    height: 28px;
    padding: 0 10px;
    font-size: 13px;
    font-weight: 500;
    color: var(--text2);
}
.grouphead:hover {
    background: var(--s2);
}
.sechead:focus-visible,
.grouphead:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.gname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.chev {
    transition: transform 0.1s;
    flex: none;
}
.chev.open {
    transform: rotate(90deg);
}
.bmhead {
    display: flex;
    align-items: center;
    justify-content: space-between;
}
.plus {
    all: unset;
    cursor: pointer;
    width: 20px;
    height: 20px;
    display: grid;
    place-items: center;
    border-radius: 4px;
    font-size: 15px;
    color: var(--text2);
}
.plus:hover {
    background: var(--s2);
}
.plus:focus-visible {
    outline: 2px solid var(--focus);
}
.bmform {
    display: flex;
    gap: 6px;
    padding: 2px 6px 6px;
}
.pbadge {
    margin-left: auto;
}
</style>
