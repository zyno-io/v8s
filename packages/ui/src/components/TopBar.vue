<script setup lang="ts">
import { computed } from 'vue';

import AccountMenu from '@/components/AccountMenu.vue';
import JobsTray from '@/components/JobsTray.vue';
import PortForwardsButton from '@/components/PortForwardsButton.vue';
import { getProblemsAll } from '@/lib/agent.ts';
import { formatCount } from '@/lib/format.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

const clusters = useClustersStore();
const problemsAll = getProblemsAll();
const problemCount = computed(() => problemsAll.total.value);
const ui = useUiStore();

const label = computed(() => {
    const ids = clusters.activeIds;
    if (!ids.length) return 'Choose clusters';
    if (ids.length === 1) return clusters.nameById.get(ids[0]) ?? ids[0];
    return `${ids.length} clusters`;
});

const dotColor = computed(() => {
    const s = clusters.states;
    if (!s.length) return 'var(--text3)';
    if (s.some(x => x.phase === 'unreachable' || x.phase === 'auth-required')) return 'var(--danger)';
    if (s.some(x => x.phase !== 'ready')) return 'var(--warning)';
    return 'var(--success)';
});

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const themeLabel = computed(() => ({ system: 'Theme: system', light: 'Theme: light', dark: 'Theme: dark' })[ui.theme]);
</script>

<template>
    <header class="topbar">
        <router-link class="brand" to="/pods">
            <span class="mark">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                    <path d="M3 3l5 10 5-10" />
                </svg>
            </span>
            <span>v8s</span>
        </router-link>
        <button
            class="btn cluster-btn"
            type="button"
            :aria-expanded="clusters.popoverOpen"
            aria-haspopup="dialog"
            @click="clusters.popoverOpen ? clusters.close() : clusters.open()"
        >
            <span class="dot" :style="{ background: dotColor }"></span>{{ label }}
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <path d="M4 6l4 4 4-4" />
            </svg>
        </button>
        <button
            class="field search"
            type="button"
            aria-haspopup="dialog"
            :aria-label="clusters.anyConnected ? 'Search objects and commands (Ctrl-K)' : 'Search (connect a cluster first)'"
            data-testid="search-btn"
            :disabled="!clusters.anyConnected"
            @click="ui.paletteOpen = true"
        >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <circle cx="7" cy="7" r="4.5" />
                <path d="M10.5 10.5L14 14" />
            </svg>
            <span class="ph">{{ clusters.anyConnected ? 'Search objects, kinds and commands' : 'Connect a cluster to search' }}</span>
            <span class="kbd">{{ isMac ? '⌘K' : 'Ctrl K' }}</span>
        </button>
        <div class="grow"></div>
        <PortForwardsButton />
        <JobsTray />
        <button class="btn ghost theme-btn" type="button" :aria-label="themeLabel" :title="themeLabel" @click="ui.cycleTheme()">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <circle cx="8" cy="8" r="3" />
                <path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4" />
            </svg>
            <span class="theme-name">{{ ui.theme }}</span>
        </button>
        <router-link
            class="tag problems"
            :class="problemCount ? 'danger' : 'success'"
            to="/problems"
            :aria-label="problemCount ? `${problemCount} problems` : 'No problems'"
            data-testid="topbar-problems"
        >
            <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                <path d="M8 2l6.5 11.5h-13z" />
                <path d="M8 6.5v3" />
            </svg>
            <span class="pcount">{{ problemCount ? formatCount(problemCount) : '0' }}</span
            ><span class="plabel">{{ problemCount === 1 ? 'problem' : 'problems' }}</span>
        </router-link>
        <AccountMenu />
    </header>
</template>

<style scoped>
.cluster-btn {
    margin-left: 8px;
}
.search {
    width: 420px;
    margin-left: 8px;
    max-width: 40vw;
    font-family: inherit;
    font-size: 13px;
    cursor: pointer;
    text-align: left;
}
.search:disabled {
    cursor: default;
    opacity: 0.6;
}
.search:disabled .kbd {
    visibility: hidden;
}
.search .ph {
    flex-grow: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.problems {
    height: 24px;
    padding: 0 8px;
}
@media (max-width: 899.98px) {
    .search {
        width: auto;
        flex: none;
    }
    .search .ph,
    .search .kbd {
        display: none;
    }
}
@media (max-width: 639.98px) {
    .topbar :deep(.theme-btn),
    .brand span:last-child {
        display: none;
    }
    .plabel {
        display: none;
    }
    .theme-name {
        display: none;
    }
    .cluster-btn {
        margin-left: 0;
    }
}
</style>
