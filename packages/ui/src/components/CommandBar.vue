<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import { useRouter } from 'vue-router';

import type { CommandContext } from '@/lib/command-parser.ts';

import { activePods, collectNamespaces } from '@/lib/active-view.ts';
import { kinds } from '@/lib/agent.ts';
import { complete, findKind, parseCommand } from '@/lib/command-parser.ts';
import { runCommand } from '@/lib/run-command.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { usePodsStore } from '@/stores/pods.ts';
import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const clusters = useClustersStore();
const pods = usePodsStore();
const router = useRouter();

const input = ref('');
const sel = ref(0);
const error = ref('');
const inputEl = ref<HTMLInputElement | null>(null);
// Namespaces are scanned once when the bar opens (and when the context is rebuilt), never per keystroke.
const namespaces = ref<string[]>([]);

const ctx = computed<CommandContext>(() => ({
    kinds: kinds.value,
    namespaces: namespaces.value,
    clusters: clusters.list.map(c => ({ id: c.id, name: c.name, groups: c.groups })),
    selected: clusters.activeIds
}));

const items = computed(() => complete(input.value, ctx.value));

/** Live hint only for a kind that is known but has no view yet. */
const hint = computed(() => {
    if (error.value) return error.value;
    const head = input.value.trim().split(/\s+/)[0];
    if (!head) return '';
    const f = findKind(head, kinds.value);
    if (f && 'kind' in f && !f.kind.available) return `${f.kind.kind} (${f.kind.plural}) is not available yet`;
    return '';
});

watch(
    () => ui.commandBarOpen,
    async open => {
        if (open) {
            input.value = '';
            error.value = '';
            sel.value = 0;
            namespaces.value = collectNamespaces(activePods.value, pods.namespaces);
            await nextTick();
            inputEl.value?.focus();
        }
    }
);
watch(input, () => {
    sel.value = 0;
    error.value = '';
});

function accept(i = sel.value) {
    const it = items.value[i];
    if (!it) return;
    input.value = it.insert;
    inputEl.value?.focus();
}

async function run() {
    let cmd = parseCommand(input.value, ctx.value);
    // "po" typed but "pods" highlighted, or a fuzzy head: run the highlighted completion when the typed text is not a command.
    if (cmd.type === 'error' && items.value[sel.value] && input.value.trim().split(/\s+/).length === 1) {
        cmd = parseCommand(items.value[sel.value].insert, ctx.value);
    }
    const err = await runCommand(cmd, router);
    if (err) error.value = err;
    else ui.commandBarOpen = false;
}

function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || (e.key === 'n' && e.ctrlKey)) {
        e.preventDefault();
        if (items.value.length) sel.value = (sel.value + 1) % items.value.length;
    } else if (e.key === 'ArrowUp' || (e.key === 'p' && e.ctrlKey)) {
        e.preventDefault();
        if (items.value.length) sel.value = (sel.value - 1 + items.value.length) % items.value.length;
    } else if (e.key === 'Tab') {
        e.preventDefault();
        accept();
    } else if (e.key === 'Enter') {
        e.preventDefault();
        void run();
    }
}
</script>

<template>
    <div v-if="ui.commandBarOpen" class="cmd floating" role="dialog" aria-label="Command bar">
        <div class="line">
            <span class="colon mono" aria-hidden="true">:</span>
            <input
                ref="inputEl"
                v-model="input"
                class="mono"
                type="text"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                aria-label="Command"
                aria-controls="cmd-list"
                aria-autocomplete="list"
                :aria-activedescendant="items.length ? `cmd-${sel}` : undefined"
                placeholder="ctx, pods, ns, pb …"
                @keydown="onKey"
            />
            <span class="kbd">esc</span>
        </div>
        <ul v-if="items.length" id="cmd-list" class="items" role="listbox">
            <li
                v-for="(it, i) in items"
                :id="`cmd-${i}`"
                :key="it.insert + i"
                role="option"
                :aria-selected="i === sel"
                :class="{ on: i === sel }"
                @mousedown.prevent="accept(i)"
            >
                <span class="mono">{{ it.label }}</span>
                <span class="muted detail">{{ it.detail }}</span>
            </li>
        </ul>
        <p v-if="hint" class="hint mono" role="status">{{ hint }}</p>
    </div>
</template>

<style scoped>
.cmd {
    position: absolute;
    z-index: 30;
    top: 8px;
    left: 20px;
    right: 20px;
    max-width: 640px;
}
.line {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 40px;
    padding: 0 12px;
}
.colon {
    color: var(--accent);
    font-size: 14px;
    font-weight: 600;
}
input {
    flex: 1;
    min-width: 0;
    border: 0;
    outline: 0;
    background: transparent;
    color: var(--text);
    font-size: 14px;
}
.line:focus-within {
    box-shadow: inset 0 0 0 2px var(--focus);
    border-radius: 8px;
}
.items {
    list-style: none;
    margin: 0;
    padding: 4px;
    border-top: 1px solid var(--border);
}
.items li {
    display: flex;
    gap: 12px;
    align-items: baseline;
    padding: 5px 8px;
    border-radius: 5px;
    cursor: pointer;
}
.items li.on {
    background: var(--accent-soft);
}
.detail {
    font-size: 12px;
}
.hint {
    margin: 0;
    padding: 6px 12px 8px;
    border-top: 1px solid var(--border);
    color: var(--danger);
}
</style>
