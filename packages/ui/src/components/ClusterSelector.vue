<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';

import { phaseColor } from '@/lib/colors.ts';
import { formatCount } from '@/lib/format.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const clusters = useClustersStore();
const filter = ref('');
const searchEl = ref<HTMLInputElement | null>(null);

const visible = computed(() => {
    const q = filter.value.trim().toLowerCase();
    if (!q) return clusters.list;
    return clusters.list.filter(
        c => c.name.toLowerCase().includes(q) || c.id.toLowerCase().includes(q) || c.groups.some(g => g.toLowerCase().includes(q))
    );
});

const groups = computed(() => {
    const m = new Map<string, string[]>();
    for (const c of clusters.list) for (const g of c.groups) m.set(g, [...(m.get(g) ?? []), c.id]);
    return [...m.entries()];
});

function stateText(id: string): string {
    const s = clusters.stateById.get(id);
    if (!s) return 'not connected';
    if (s.phase === 'syncing') return `syncing · ${formatCount(s.synced)}`;
    return s.phase;
}

function pickGroup(ids: string[], e: MouseEvent) {
    if (e.shiftKey) clusters.setChecked([...clusters.checked, ...ids]);
    else clusters.setChecked(ids);
}

const note = computed(() => {
    const n = clusters.checked.size;
    const same = clusters.activeIds.length === 0 && n > 0 && [...clusters.checked].every(id => clusters.remembered.includes(id));
    return `${n} selected${same ? ' · remembered from last time' : ''}`;
});

watch(
    () => clusters.popoverOpen,
    async open => {
        if (open) {
            filter.value = '';
            await nextTick();
            searchEl.value?.focus();
        } else {
            const sel = clusters.takeFocusTarget() === 'table' ? '.scroller' : '.cluster-btn';
            document.querySelector<HTMLElement>(sel)?.focus();
        }
    }
);

function connect() {
    void clusters.connect([...clusters.checked]);
}
</script>

<template>
    <template v-if="clusters.popoverOpen">
        <div class="backdrop" @mousedown="clusters.close()"></div>
        <section class="floating panel" role="dialog" aria-label="Choose clusters">
            <div class="head">
                <template v-if="groups.length">
                    <div class="hl">
                        <span class="lbl">Groups</span>
                        <span class="muted small">click selects a group, shift-click adds it</span>
                    </div>
                    <div class="gchips">
                        <button v-for="[g, ids] in groups" :key="g" class="chip" type="button" @click="pickGroup(ids, $event)">
                            {{ g }} <span class="muted">{{ ids.length }}</span>
                        </button>
                    </div>
                </template>
                <label class="field">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                        <circle cx="7" cy="7" r="4.5" />
                        <path d="M10.5 10.5L14 14" />
                    </svg>
                    <input
                        ref="searchEl"
                        v-model="filter"
                        type="search"
                        :placeholder="`Filter ${clusters.list.length} contexts`"
                        aria-label="Filter contexts"
                    />
                </label>
            </div>
            <ul class="list">
                <li v-for="c in visible" :key="c.id">
                    <label class="crow" :class="{ on: clusters.checked.has(c.id) }">
                        <input class="cb" type="checkbox" :checked="clusters.checked.has(c.id)" @change="clusters.toggle(c.id)" />
                        <span class="dot" :style="{ background: phaseColor(clusters.stateById.get(c.id)?.phase ?? '') }"></span>
                        <span class="ctext">
                            <span class="cname">{{ c.name }}<span v-for="g in c.groups" :key="g" class="tag">{{ g }}</span></span>
                            <span class="muted small mono">{{ c.server }}</span>
                        </span>
                        <span class="muted small state">{{ stateText(c.id) }}</span>
                    </label>
                </li>
                <li v-if="!visible.length" class="empty muted">No matching contexts</li>
            </ul>
            <p v-if="clusters.error" class="err" role="alert">{{ clusters.error }}</p>
            <div class="foot">
                <span class="small note">{{ note }}</span>
                <div class="grow"></div>
                <button class="btn" type="button" @click="clusters.close()">Cancel</button>
                <button class="btn primary" type="button" :disabled="clusters.connecting" @click="connect">
                    {{ clusters.connecting ? 'Connecting…' : 'Connect' }}
                </button>
            </div>
        </section>
    </template>
</template>

<style scoped>
.backdrop {
    position: fixed;
    inset: 0;
    z-index: 40;
}
.panel {
    position: fixed;
    z-index: 41;
    top: 52px;
    left: 92px;
    width: min(520px, calc(100vw - 16px));
    max-height: calc(100vh - 68px);
    display: flex;
    flex-direction: column;
}
.head {
    padding: 14px 14px 10px;
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.hl {
    display: flex;
    align-items: center;
    gap: 8px;
}
.small {
    font-size: 12px;
}
.gchips {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
}
.list {
    list-style: none;
    margin: 0;
    padding: 6px;
    border-top: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 2px;
    overflow: auto;
    min-height: 0;
}
.crow {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 40px;
    padding: 6px 12px;
    border-radius: 6px;
    cursor: pointer;
}
.crow.on,
.crow:hover {
    background: var(--hover);
}
.crow:has(:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.ctext {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex-grow: 1;
}
.cname {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 600;
}
.state {
    white-space: nowrap;
}
.empty {
    padding: 12px;
    text-align: center;
}
.err {
    margin: 0 12px 8px;
    color: var(--danger);
    font-size: 12px;
}
.foot {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 14px;
    border-top: 1px solid var(--border);
    background: var(--bg);
    border-radius: 0 0 8px 8px;
}
.note {
    color: var(--text2);
}
@media (max-width: 639.98px) {
    .panel {
        left: 8px;
        top: 52px;
    }
}
</style>
