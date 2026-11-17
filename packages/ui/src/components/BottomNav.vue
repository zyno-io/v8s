<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import { useRoute } from 'vue-router';

import SideNav from '@/components/SideNav.vue';
import { getProblemsAll, kinds } from '@/lib/agent.ts';
import { formatCount } from '@/lib/format.ts';
import { buildSidebar } from '@/lib/kinds.ts';
import { gvrPath } from '@/lib/route.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

type Sheet = '' | 'starred' | 'more';

const route = useRoute();
const ui = useUiStore();
const clusters = useClustersStore();
const settings = useSettingsStore();
const problems = getProblemsAll();
const count = computed(() => problems.total.value);
const starred = computed(() => buildSidebar(kinds.value, settings.stars).starred);

const sheet = ref<Sheet>('');
const sheetEl = ref<HTMLElement | null>(null);
function toggle(s: Sheet) {
    sheet.value = sheet.value === s ? '' : s;
}
// Any navigation closes the sheet.
watch(
    () => route.fullPath,
    () => (sheet.value = '')
);
watch(sheet, async s => {
    if (!s) return;
    await nextTick();
    sheetEl.value?.querySelector<HTMLElement>('a, button')?.focus();
});

function openClusters() {
    sheet.value = '';
    if (clusters.popoverOpen) clusters.close();
    else clusters.open();
}
function openSearch() {
    sheet.value = '';
    ui.paletteOpen = true;
}
const on = (prefix: string) => route.path === prefix || route.path.startsWith(prefix + '/');
</script>

<template>
    <div class="bn-wrap">
        <template v-if="sheet">
            <div class="scrim" @click="sheet = ''"></div>
            <div
                ref="sheetEl"
                class="sheet floating"
                role="dialog"
                :aria-label="sheet === 'starred' ? 'Starred types' : 'More'"
                data-testid="bottom-sheet"
                @keydown.esc.stop="sheet = ''"
            >
                <template v-if="sheet === 'starred'">
                    <h2>Starred</h2>
                    <p v-if="!starred.length" class="muted">Star a type from its list header to pin it here.</p>
                    <router-link v-for="s in starred" :key="s.gvr" class="item" :to="s.to ?? gvrPath(s.gvr)">{{ s.label }}</router-link>
                </template>
                <template v-else>
                    <div class="mrow">
                        <h2>More</h2>
                        <button class="btn" type="button" @click="ui.cycleTheme()">Theme: {{ ui.theme }}</button>
                    </div>
                    <SideNav class="sheet-nav" />
                </template>
            </div>
        </template>
        <nav class="bn" aria-label="Main">
            <router-link class="bi" :class="{ on: on('/problems') }" to="/problems" data-testid="bn-problems">
                <svg width="20" height="20" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                    <path d="M8 2l6.5 11.5h-13z" />
                    <path d="M8 6.5v3" />
                    <path d="M8 11.5v.2" />
                </svg>
                <span>Problems</span>
                <span v-if="count" class="badge tag danger">{{ formatCount(count) }}</span>
            </router-link>
            <button class="bi" type="button" :aria-expanded="clusters.popoverOpen" data-testid="bn-clusters" @click="openClusters">
                <svg width="20" height="20" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                    <rect x="2" y="3" width="12" height="4" rx="1" />
                    <rect x="2" y="9" width="12" height="4" rx="1" />
                </svg>
                <span>Clusters</span>
            </button>
            <button class="bi" type="button" aria-haspopup="dialog" data-testid="bn-search" @click="openSearch">
                <svg width="20" height="20" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" />
                    <path d="M10.5 10.5L14 14" />
                </svg>
                <span>Search</span>
            </button>
            <button
                class="bi"
                :class="{ on: sheet === 'starred' }"
                type="button"
                :aria-expanded="sheet === 'starred'"
                data-testid="bn-starred"
                @click="toggle('starred')"
            >
                <svg width="20" height="20" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                    <path d="M8 1.8l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.4 4.1 13.6 5 9.3l-3.2-3 4.3-.5z" />
                </svg>
                <span>Starred</span>
            </button>
            <button
                class="bi"
                :class="{ on: sheet === 'more' }"
                type="button"
                :aria-expanded="sheet === 'more'"
                data-testid="bn-more"
                @click="toggle('more')"
            >
                <svg width="20" height="20" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
                    <circle cx="3.5" cy="8" r="1.3" />
                    <circle cx="8" cy="8" r="1.3" />
                    <circle cx="12.5" cy="8" r="1.3" />
                </svg>
                <span>More</span>
            </button>
        </nav>
    </div>
</template>

<style scoped>
.bn-wrap {
    display: none;
}
@media (max-width: 639.98px) {
    .bn-wrap {
        display: block;
        flex: none;
    }
}
.bn {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    height: calc(56px + env(safe-area-inset-bottom, 0px));
    padding-bottom: env(safe-area-inset-bottom, 0px);
    background: var(--surface);
    border-top: 1px solid var(--border);
    position: relative;
    z-index: 26;
}
.bi {
    all: unset;
    box-sizing: border-box;
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
    min-height: 44px;
    font-size: 11px;
    font-weight: 500;
    color: var(--text3);
    cursor: pointer;
    text-decoration: none;
}
.bi.on {
    color: var(--accent);
}
.bi:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -3px;
}
.badge {
    position: absolute;
    top: 4px;
    left: calc(50% + 6px);
    height: 16px;
    padding: 0 5px;
    font-size: 10px;
}
.scrim {
    position: fixed;
    inset: 0;
    z-index: 27;
    background: rgba(12, 13, 16, 0.35);
}
.sheet {
    position: fixed;
    left: 0;
    right: 0;
    bottom: calc(56px + env(safe-area-inset-bottom, 0px));
    z-index: 28;
    max-height: 75vh;
    overflow: auto;
    padding: 12px 12px 16px;
    border-radius: 14px 14px 0 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
}
h2 {
    margin: 0 0 4px;
    font-size: 15px;
    font-weight: 650;
}
.mrow {
    display: flex;
    align-items: center;
    justify-content: space-between;
}
.item {
    display: flex;
    align-items: center;
    min-height: 44px;
    padding: 0 10px;
    border-radius: 8px;
    color: var(--text);
    text-decoration: none;
    font-size: 15px;
}
.item:hover,
.item:focus-visible {
    background: var(--hover);
}
/* The full resource navigation, shown here although the sidebar itself is hidden on narrow screens. */
.sheet :deep(.side) {
    display: block !important;
    width: auto;
    border: 0;
    padding: 0;
    overflow: visible;
    background: transparent;
}
.sheet :deep(.nav) {
    min-height: 40px;
    font-size: 15px;
}
</style>
