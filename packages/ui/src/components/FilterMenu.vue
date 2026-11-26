<script setup lang="ts">
import { computed, nextTick, ref } from 'vue';

import SelectedChips from '@/components/SelectedChips.vue';

// A multi-select filter popover (the namespace and node filters): button, search, checklist, the picks pinned below the list, Clear and
// Done. The owner supplies the options and keeps the selection; `open` and `query` are models so it can load options only while open
// and narrow them in the agent as the user types.
const props = defineProps<{
    /** Button prefix and dialog name: "Namespace" gives "Namespace: all" and the "Namespace filter" dialog. */
    label: string;
    /** Plural for the search field and the empty state: "namespaces". */
    noun: string;
    /** Every option to list, sorted; the picks are included so they stay checkable. Filtered here by the search text. */
    options: readonly string[];
    selected: readonly string[];
    /** Replaces "No <noun> loaded yet" when there are no options (e.g. the list is not allowed). */
    note?: string;
}>();
const emit = defineEmits<{ change: [list: string[]] }>();
const open = defineModel<boolean>('open', { default: false });
const q = defineModel<string>('query', { default: '' });

const searchEl = ref<HTMLInputElement | null>(null);
const btnEl = ref<HTMLButtonElement | null>(null);
// The popover hangs from its button's left edge; a button far right (the Node menu on a phone) shifts it left so it stays on screen.
const MENU_W = 260;
const GUTTER = 12;
const place = ref<{ left: string; width: string }>({ left: '0px', width: MENU_W + 'px' });
function measure() {
    const r = btnEl.value?.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    const width = Math.min(MENU_W, vw - 2 * GUTTER);
    const left = r ? Math.min(0, vw - GUTTER - width - r.left) : 0;
    place.value = { left: left + 'px', width: width + 'px' };
}

const value = computed(() => {
    const n = props.selected;
    return n.length === 0 ? 'all' : n.length === 1 ? n[0] : `${n.length} selected`;
});
const shown = computed(() => {
    const t = q.value.trim().toLowerCase();
    return t ? props.options.filter(n => n.toLowerCase().includes(t)) : props.options;
});
const picked = computed(() => props.selected.map(v => ({ key: v, label: v })));
const emptyText = computed(() => (props.options.length || q.value.trim() ? 'No matches' : (props.note ?? `No ${props.noun} loaded yet`)));

async function toggleMenu() {
    if (open.value) return close();
    q.value = '';
    measure();
    open.value = true;
    await nextTick();
    searchEl.value?.focus();
}
function close() {
    open.value = false;
    btnEl.value?.focus();
}
function toggle(v: string) {
    const cur = props.selected;
    emit('change', cur.includes(v) ? cur.filter(x => x !== v) : [...cur, v]);
}
</script>

<template>
    <div class="wrap" @keydown.esc.stop="open && close()">
        <button ref="btnEl" class="btn" type="button" aria-haspopup="listbox" :aria-expanded="open" @click="toggleMenu">
            {{ label }}: {{ value }}
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <path d="M4 6l4 4 4-4" />
            </svg>
        </button>
        <template v-if="open">
            <div class="backdrop" @mousedown="close"></div>
            <!-- Digits pick recent namespace sets (lib/keys.ts); not while a menu is choosing by hand. -->
            <div
                class="floating menu"
                :style="place"
                role="dialog"
                :aria-label="`${label} filter`"
                @keydown="/^[0-9]$/.test($event.key) && $event.stopPropagation()"
            >
                <label class="field">
                    <input ref="searchEl" v-model="q" type="search" :placeholder="`Search ${noun}`" :aria-label="`Search ${noun}`" />
                </label>
                <ul>
                    <li v-for="v in shown" :key="v">
                        <label class="opt"><input class="cb" type="checkbox" :checked="selected.includes(v)" @change="toggle(v)" />{{ v }}</label>
                    </li>
                    <li v-if="!shown.length" class="muted none">{{ emptyText }}</li>
                </ul>
                <SelectedChips :items="picked" label="Selected" @remove="toggle" />
                <div class="foot">
                    <button class="btn ghost sm" type="button" :disabled="!selected.length" @click="emit('change', [])">Clear</button>
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
    margin-top: 8px;
}
</style>
