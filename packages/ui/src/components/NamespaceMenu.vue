<script setup lang="ts">
import type { ViewHandle } from '@v8s/client';

import { computed, nextTick, ref } from 'vue';

import { collectNamespaces } from '@/lib/active-view.ts';
import { usePodsStore } from '@/stores/pods.ts';

const props = defineProps<{ handle: ViewHandle }>();
const pods = usePodsStore();

const open = ref(false);
const q = ref('');
const all = ref<string[]>([]);
const searchEl = ref<HTMLInputElement | null>(null);
const btnEl = ref<HTMLButtonElement | null>(null);

const label = computed(() => {
    const n = pods.namespaces;
    return n.length === 0 ? 'all' : n.length === 1 ? n[0] : `${n.length} selected`;
});
const shown = computed(() => {
    const t = q.value.trim().toLowerCase();
    return t ? all.value.filter(n => n.toLowerCase().includes(t)) : all.value;
});

async function toggleMenu() {
    open.value = !open.value;
    if (open.value) {
        // Scan the held window once per open; the list is a convenience, the agent filter is authoritative.
        all.value = collectNamespaces(props.handle, pods.namespaces);
        q.value = '';
        await nextTick();
        searchEl.value?.focus();
    }
}
function close() {
    open.value = false;
    btnEl.value?.focus();
}
function toggle(ns: string) {
    pods.namespaces = pods.namespaces.includes(ns) ? pods.namespaces.filter(x => x !== ns) : [...pods.namespaces, ns];
}
</script>

<template>
    <div class="wrap" @keydown.esc.stop="open && close()">
        <button ref="btnEl" class="btn" type="button" aria-haspopup="listbox" :aria-expanded="open" @click="toggleMenu">
            Namespace: {{ label }}
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <path d="M4 6l4 4 4-4" />
            </svg>
        </button>
        <template v-if="open">
            <div class="backdrop" @mousedown="close"></div>
            <div class="floating menu" role="dialog" aria-label="Namespace filter">
                <label class="field">
                    <input ref="searchEl" v-model="q" type="search" placeholder="Search namespaces" aria-label="Search namespaces" />
                </label>
                <ul>
                    <li v-for="ns in shown" :key="ns">
                        <label class="opt"
                            ><input class="cb" type="checkbox" :checked="pods.namespaces.includes(ns)" @change="toggle(ns)" />{{ ns }}</label
                        >
                    </li>
                    <li v-if="!shown.length" class="muted none">{{ all.length ? 'No matches' : 'No namespaces loaded yet' }}</li>
                </ul>
                <div class="foot">
                    <button class="btn ghost sm" type="button" :disabled="!pods.namespaces.length" @click="pods.namespaces = []">Clear</button>
                    <div class="grow"></div>
                    <button class="btn sm" type="button" @click="close">Done</button>
                </div>
            </div>
        </template>
    </div>
</template>

<style scoped>
.wrap {
    position: relative;
}
.backdrop {
    position: fixed;
    inset: 0;
    z-index: 20;
}
.menu {
    position: absolute;
    z-index: 21;
    top: 36px;
    left: 0;
    width: 260px;
    padding: 8px;
}
ul {
    list-style: none;
    margin: 8px 0;
    padding: 0;
    max-height: 260px;
    overflow: auto;
}
.opt {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 28px;
    padding: 0 6px;
    border-radius: 5px;
    font-size: 13px;
    cursor: pointer;
}
.opt:hover {
    background: var(--hover);
}
.opt:has(:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.none {
    padding: 6px;
    font-size: 12px;
}
.foot {
    display: flex;
    align-items: center;
}
</style>
