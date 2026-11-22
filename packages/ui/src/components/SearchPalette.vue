<script setup lang="ts">
import type { IPSearch, SearchHit, SearchResult } from '@v8s/client';

import { computed, nextTick, onMounted, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import ProtectedMark from '@/components/ProtectedMark.vue';
import { getAgent, kinds } from '@/lib/agent.ts';
import { clusterColor } from '@/lib/colors.ts';
import { openObject } from '@/lib/drawer.ts';
import { rank } from '@/lib/fuzzy.ts';
import { ipItems, looksLikeIP, pendingText, skippedText } from '@/lib/ip-search.ts';
import { kindLabel } from '@/lib/kinds.ts';
import { gvrPath } from '@/lib/route.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

interface Item {
    key: string;
    section: string;
    label: string;
    detail?: string;
    /** A matched address: mono chip, bold when `strong`. */
    value?: string;
    strong?: boolean;
    testid?: string;
    cluster?: string;
    run: () => void;
}

const ui = useUiStore();
const clusters = useClustersStore();
const table = useTableStore();
const router = useRouter();
const input = ref('');
const sel = ref(0);
const inputEl = ref<HTMLInputElement | null>(null);
const hits = ref<SearchHit[]>([]);
const ipRes = shallowRef<IPSearch | undefined>();
const searching = ref(false);

onMounted(async () => {
    if (ui.paletteQuery) {
        input.value = ui.paletteQuery;
        ui.paletteQuery = '';
    }
    await nextTick();
    inputEl.value?.focus();
});

function close() {
    ui.paletteOpen = false;
}

const commands = computed<Item[]>(() => [
    {
        key: 'c-ctx',
        section: 'Commands',
        label: 'Choose clusters',
        detail: ':ctx',
        run: () => {
            close();
            clusters.open();
        }
    },
    {
        key: 'c-create',
        section: 'Commands',
        label: 'Create a resource',
        detail: 'from YAML or a template',
        run: () => {
            close();
            ui.createOpen = true;
        }
    },
    {
        key: 'c-theme',
        section: 'Commands',
        label: 'Cycle theme',
        detail: ui.theme,
        run: () => {
            ui.cycleTheme();
            close();
        }
    },
    {
        key: 'c-pb',
        section: 'Commands',
        label: 'Open Problems',
        detail: ':pb',
        run: () => {
            close();
            void router.push('/problems');
        }
    },
    {
        key: 'c-keys',
        section: 'Commands',
        label: 'Keyboard shortcuts',
        detail: '?',
        run: () => {
            close();
            ui.shortcutsOpen = true;
        }
    }
]);

const kindItems = computed<Item[]>(() =>
    kinds.value
        .filter(k => k.available)
        .map(k => ({
            key: 'k-' + k.gvr,
            section: 'Kinds' as const,
            label: kindLabel(k),
            detail: k.group ? `${k.kind} · ${k.group}` : k.kind,
            run: () => {
                close();
                void router.push(gvrPath(k.gvr));
            }
        }))
);

const isIP = computed(() => looksLikeIP(input.value));

const ipRows = computed<Item[]>(() =>
    ipRes.value
        ? ipItems(ipRes.value).map(it => ({
              key: it.key,
              section: it.section,
              label: it.label,
              detail: it.detail,
              value: it.value,
              strong: it.strong,
              testid: it.testid,
              cluster: it.cluster,
              run: () => {
                  close();
                  const a = it.action;
                  if (a.type === 'open') {
                      void openObject(router, a.ref);
                  } else {
                      table.clearFilters(a.gvr);
                      table.of(a.gvr).text = a.text;
                      void router.push(gvrPath(a.gvr));
                  }
              }
          }))
        : []
);

const notes = computed(() => [pendingText(ipRes.value), skippedText(ipRes.value)].filter(Boolean));

const items = computed<Item[]>(() => {
    const q = input.value.trim();
    const objects: Item[] = hits.value.map(h => ({
        key: `o-${h.cluster}-${h.gvr}-${h.namespace}-${h.name}`,
        section: 'Objects',
        label: h.name,
        detail: `${h.kind}${h.namespace ? ' · ' + h.namespace : ''}`,
        cluster: h.cluster,
        run: () => {
            close();
            void openObject(router, { cluster: h.cluster, gvr: h.gvr, namespace: h.namespace || undefined, name: h.name });
        }
    }));
    if (isIP.value) return [...ipRows.value, ...objects]; // Kinds and Commands are noise while typing an address
    const k = q ? rank(q, kindItems.value, i => i.label + ' ' + i.detail).slice(0, 6) : kindItems.value.slice(0, 5);
    const c = q ? rank(q, commands.value, i => i.label) : commands.value;
    return [...objects, ...k, ...c];
});

watch(items, () => {
    if (sel.value >= items.value.length) sel.value = 0;
});

let timer: ReturnType<typeof setTimeout> | undefined;
let seq = 0;
watch(input, q => {
    sel.value = 0;
    clearTimeout(timer);
    // Results belong to the previous text: drop them so Enter can never open a stale hit.
    hits.value = [];
    ipRes.value = undefined;
    seq++;
    const text = q.trim();
    if (text.length < 2) {
        searching.value = false;
        return;
    }
    searching.value = true;
    timer = setTimeout(() => void search(text, 0), 150);
});

// Sources that are still loading (pending) are asked again shortly, a few times at most.
const MAX_RETRIES = 5;
async function search(text: string, attempt: number) {
    const my = ++seq;
    let again = false;
    try {
        const res = await getAgent().call<SearchResult>('search', { text, limit: 20, ipPerKind: 5 });
        if (my !== seq) return;
        hits.value = res.results ?? [];
        ipRes.value = res.ip;
        searching.value = false; // Enter works on what is shown; the re-asks below only refine it
        again = !!res.ip?.pending?.length && attempt < MAX_RETRIES;
    } catch {
        if (my === seq) {
            hits.value = [];
            ipRes.value = undefined;
        }
    } finally {
        if (my === seq) searching.value = false;
    }
    if (again) {
        timer = setTimeout(() => {
            if (my === seq) void search(text, attempt + 1);
        }, 600);
    }
}

watch(
    () => ui.paletteOpen,
    () => clearTimeout(timer)
);

function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || (e.key === 'n' && e.ctrlKey)) {
        e.preventDefault();
        if (items.value.length) sel.value = (sel.value + 1) % items.value.length;
    } else if (e.key === 'ArrowUp' || (e.key === 'p' && e.ctrlKey)) {
        e.preventDefault();
        if (items.value.length) sel.value = (sel.value - 1 + items.value.length) % items.value.length;
    } else if (e.key === 'Enter') {
        e.preventDefault();
        if (searching.value) return;
        items.value[sel.value]?.run();
    } else if (e.key === 'Escape') {
        e.preventDefault();
        close();
    }
}

const sectionStart = (i: number) => i === 0 || items.value[i - 1].section !== items.value[i].section;
const multi = computed(() => clusters.activeIds.length > 1);
</script>

<template>
    <div class="backdrop" @mousedown="close"></div>
    <section class="pal floating" role="dialog" aria-label="Search" aria-modal="true">
        <div class="line">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <circle cx="7" cy="7" r="4.5" />
                <path d="M10.5 10.5L14 14" />
            </svg>
            <input
                ref="inputEl"
                v-model="input"
                type="text"
                role="combobox"
                aria-expanded="true"
                aria-controls="pal-list"
                aria-autocomplete="list"
                :aria-activedescendant="items.length ? `pal-${sel}` : undefined"
                autocomplete="off"
                spellcheck="false"
                placeholder="Search objects, kinds and commands"
                aria-label="Search"
                data-testid="palette-input"
                @keydown="onKey"
            />
            <span class="kbd">esc</span>
        </div>
        <ul id="pal-list" class="items" role="listbox">
            <template v-for="(it, i) in items" :key="it.key">
                <li v-if="sectionStart(i)" class="sec lbl" role="presentation">{{ it.section }}</li>
                <li
                    :id="`pal-${i}`"
                    role="option"
                    :aria-selected="i === sel"
                    :class="{ on: i === sel }"
                    :data-testid="it.testid"
                    @mousedown.prevent="it.run()"
                    @mousemove="sel = i"
                >
                    <span v-if="it.cluster && multi" class="tag" :class="clusters.tagTone(it.cluster)"
                        ><span class="dot" :style="{ background: clusterColor(it.cluster) }"></span>{{ clusters.displayName(it.cluster)
                        }}<ProtectedMark :cluster="it.cluster"
                    /></span>
                    <span class="lab">{{ it.label }}</span>
                    <span v-if="it.value" class="val mono" :class="{ strong: it.strong }">{{ it.value }}</span>
                    <span class="muted detail">{{ it.detail }}</span>
                </li>
            </template>
            <li v-if="!items.length" class="muted none" role="presentation">{{ searching ? 'Searching…' : 'No matches' }}</li>
            <li v-for="n in notes" :key="n" class="muted note" role="presentation">{{ n }}</li>
        </ul>
    </section>
</template>

<style scoped>
.backdrop {
    position: fixed;
    inset: 0;
    z-index: 70;
    background: rgba(12, 13, 16, 0.25);
}
.pal {
    position: fixed;
    z-index: 71;
    top: 12vh;
    left: 50%;
    transform: translateX(-50%);
    width: min(620px, calc(100vw - 16px));
    max-height: 70vh;
    display: flex;
    flex-direction: column;
}
.line {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 46px;
    padding: 0 14px;
    color: var(--text3);
    border-bottom: 1px solid var(--border);
}
input {
    flex: 1;
    min-width: 0;
    border: 0;
    outline: 0;
    background: transparent;
    color: var(--text);
    font-family: inherit;
    font-size: 14px;
}
.line:focus-within {
    box-shadow: inset 0 0 0 2px var(--focus);
    border-radius: 8px 8px 0 0;
}
.items {
    list-style: none;
    margin: 0;
    padding: 4px;
    overflow: auto;
}
.sec {
    padding: 8px 8px 2px;
}
.items li[role='option'] {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 8px;
    border-radius: 5px;
    cursor: pointer;
    min-height: 32px;
}
.items li.on {
    background: var(--accent-soft);
}
.lab {
    font-size: 13px;
    font-weight: 500;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.detail {
    font-size: 12px;
    margin-left: auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.val {
    font-size: 12px;
    color: var(--text2);
    white-space: nowrap;
}
.val.strong {
    font-weight: 600;
    color: var(--text);
}
.none {
    padding: 12px;
    text-align: center;
}
.note {
    padding: 6px 8px 4px;
    font-size: 12px;
}
</style>
