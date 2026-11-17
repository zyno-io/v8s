<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';

import { linkAllPatch, pairOptions } from '@/lib/link-ops.ts';
import { suggestLinks } from '@/lib/link-suggest.ts';
import { errorText } from '@/lib/mutations.ts';
import { dismissedLinks, dismissLinkPatch } from '@/lib/settings.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const emit = defineEmits<{ close: [] }>();

const clusters = useClustersStore();
const settings = useSettingsStore();
const ui = useUiStore();
const el = ref<HTMLDialogElement | null>(null);
const err = ref('');
const read = ref('');
const write = ref('');
let returnFocus: HTMLElement | null = null;

const nameOf = (id: string) => clusters.nameById.get(id) ?? id;
const candidates = computed(() => clusters.list.map(c => ({ id: c.id, name: c.name })));
const current = computed(() => Object.entries(settings.links).sort((a, b) => nameOf(a[0]).localeCompare(nameOf(b[0]))));
const suggestions = computed(() => suggestLinks(candidates.value, settings.links, dismissedLinks(settings.doc)));
const readOptions = computed(() => pairOptions(candidates.value, settings.links, write.value));
const writeOptions = computed(() => pairOptions(candidates.value, settings.links, read.value));

onMounted(async () => {
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    await nextTick();
    if (!el.value?.open) el.value?.showModal();
    el.value?.querySelector<HTMLElement>('button, select, input')?.focus();
});
onBeforeUnmount(() => {
    if (el.value?.open) el.value.close();
    returnFocus?.focus?.();
});

async function guard(fn: () => Promise<unknown>) {
    err.value = '';
    try {
        await fn();
        await clusters.refreshList();
    } catch (e) {
        err.value = errorText(e);
    }
}
function uncheck(writes: ReadonlySet<string>) {
    clusters.setChecked([...clusters.checked].filter(id => !writes.has(id)));
}
function link(r: string, w: string) {
    if (!r || !w || r === w) return;
    void guard(async () => {
        await settings.patch({ links: { [r]: w } });
        uncheck(new Set([w]));
    });
}
function linkManual() {
    link(read.value, write.value);
    read.value = '';
    write.value = '';
}
function linkAll() {
    const { patch, writes } = linkAllPatch(suggestions.value);
    if (!writes.size) return;
    void guard(async () => {
        await settings.patch(patch);
        uncheck(writes);
    });
}
function unlink(r: string) {
    void guard(() => settings.patch({ links: { [r]: null } }));
}
function dismiss(r: string, w: string) {
    void guard(() => settings.patch(dismissLinkPatch(r, w)));
}
function setAutoSwitch(e: Event) {
    const on = (e.target as HTMLInputElement).checked;
    settings.patch({ autoSwitchWrite: on }).catch(x => ui.toast('danger', errorText(x)));
}
</script>

<template>
    <dialog
        ref="el"
        class="dlg floating"
        aria-labelledby="lc-title"
        data-testid="linked-dialog"
        @cancel.prevent="emit('close')"
        @click.self="emit('close')"
    >
        <div class="body">
            <h2 id="lc-title">Linked contexts</h2>
            <p class="muted intro">
                A linked pair behaves as one cluster: reads use the read context, and actions that need more run through the write context.
            </p>

            <section aria-labelledby="lc-current">
                <h3 id="lc-current">Current links</h3>
                <ul v-if="current.length" class="rows" data-testid="lc-current">
                    <li v-for="[r, w] in current" :key="r" class="r">
                        <span class="pair"
                            ><span class="mono">{{ nameOf(r) }}</span><span class="arrow" aria-hidden="true">→</span
                            ><span class="mono">{{ nameOf(w) }}</span></span
                        >
                        <button class="btn ghost sm" type="button" :aria-label="`Unlink ${nameOf(r)} from ${nameOf(w)}`" @click="unlink(r)">
                            Unlink
                        </button>
                    </li>
                </ul>
                <p v-else class="muted small">No linked contexts.</p>
            </section>

            <section v-if="suggestions.length" aria-labelledby="lc-sugg">
                <div class="sh">
                    <h3 id="lc-sugg">Suggestions</h3>
                    <button class="btn sm" type="button" data-testid="lc-link-all" @click="linkAll">Link all ({{ suggestions.length }})</button>
                </div>
                <ul class="rows" data-testid="lc-suggestions">
                    <li v-for="s in suggestions" :key="s.read + s.write" class="r">
                        <span class="pair"
                            ><span class="mono">{{ nameOf(s.read) }}</span><span class="arrow" aria-hidden="true">→</span
                            ><span class="mono">{{ nameOf(s.write) }}</span></span
                        >
                        <button
                            class="btn sm"
                            type="button"
                            :aria-label="`Link ${nameOf(s.read)} to ${nameOf(s.write)}`"
                            @click="link(s.read, s.write)"
                        >
                            Link
                        </button>
                        <button
                            class="btn ghost sm"
                            type="button"
                            :aria-label="`Dismiss ${nameOf(s.read)} to ${nameOf(s.write)}`"
                            @click="dismiss(s.read, s.write)"
                        >
                            Dismiss
                        </button>
                    </li>
                </ul>
            </section>

            <p v-if="!current.length && !suggestions.length" class="muted small" data-testid="lc-empty">
                No linked contexts and no suggestions. Link a pair below.
            </p>

            <section aria-labelledby="lc-manual">
                <h3 id="lc-manual">Link a pair…</h3>
                <form class="manual" @submit.prevent="linkManual">
                    <label class="sel"
                        ><span class="muted small">Read context</span>
                        <select v-model="read" data-testid="lc-read">
                            <option value="">Choose…</option>
                            <option v-for="o in readOptions" :key="o.id" :value="o.id">{{ o.name }}</option>
                        </select></label
                    >
                    <label class="sel"
                        ><span class="muted small">Write context</span>
                        <select v-model="write" data-testid="lc-write">
                            <option value="">Choose…</option>
                            <option v-for="o in writeOptions" :key="o.id" :value="o.id">{{ o.name }}</option>
                        </select></label
                    >
                    <button class="btn sm" type="submit" :disabled="!read || !write" data-testid="lc-link">Link</button>
                </form>
            </section>

            <label class="auto small"
                ><input class="cb" type="checkbox" :checked="settings.autoSwitchWrite" @change="setAutoSwitch" />Switch to the linked write context
                automatically when the read context is denied</label
            >
            <p v-if="err" class="err" role="alert">{{ err }}</p>
            <div class="foot">
                <button class="btn primary" type="button" @click="emit('close')">Done</button>
            </div>
        </div>
    </dialog>
</template>

<style scoped>
.dlg {
    padding: 0;
    width: min(520px, calc(100vw - 24px));
    max-height: calc(100vh - 24px);
    color: var(--text);
    border-radius: 12px;
}
.dlg::backdrop {
    background: rgba(12, 13, 16, 0.45);
}
.body {
    padding: 18px 18px 14px;
    display: flex;
    flex-direction: column;
    gap: 14px;
}
h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
}
h3 {
    margin: 0 0 6px;
    font-size: 12px;
    font-weight: 600;
    color: var(--text2);
}
p {
    margin: 0;
    font-size: 13px;
}
.small {
    font-size: 12px;
}
.sh {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 6px;
}
.sh h3 {
    margin: 0;
}
.rows {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
}
.r {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 32px;
    padding: 2px 8px;
    border-radius: 6px;
    background: var(--hover);
}
.pair {
    flex-grow: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    font-size: 12px;
    word-break: break-all;
}
.arrow {
    color: var(--text3);
}
.manual {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    flex-wrap: wrap;
}
.sel {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1 1 150px;
    min-width: 0;
}
select {
    font-family: inherit;
    font-size: 12px;
    height: 28px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
    color: var(--text);
    min-width: 0;
}
.auto {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text2);
}
.err {
    color: var(--danger, #c0392b);
    font-size: 12px;
}
.foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
}
@media (pointer: coarse) {
    .r {
        min-height: 44px;
    }
    select {
        height: 40px;
    }
}
</style>
