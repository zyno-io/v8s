<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';

import { phaseColor } from '@/lib/colors.ts';
import { formatCount } from '@/lib/format.ts';
import { suggestLinks } from '@/lib/link-suggest.ts';
import { errorText } from '@/lib/mutations.ts';
import {
    deleteGroupPatch,
    dismissedLinks,
    dismissLinkPatch,
    groupList,
    groupPatch,
    productionGroupsPatch,
    renameGroupPatch
} from '@/lib/settings.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const clusters = useClustersStore();
const settings = useSettingsStore();
const ui = useUiStore();
const filter = ref('');
const searchEl = ref<HTMLInputElement | null>(null);

const visible = computed(() => {
    const q = filter.value.trim().toLowerCase();
    if (!q) return clusters.list;
    return clusters.list.filter(
        c => c.name.toLowerCase().includes(q) || c.id.toLowerCase().includes(q) || c.groups.some(g => g.toLowerCase().includes(q))
    );
});

const groups = computed(() => groupList(settings.groups).map(([g, ids]) => [g, ids.filter(id => clusters.byId.has(id))] as [string, string[]]));
function stateText(id: string): string {
    const s = clusters.stateById.get(id);
    if (!s) return 'not connected';
    if (s.phase === 'syncing') return `syncing · ${formatCount(s.synced)}`;
    if (s.phase === 'auth-required') return 'sign-in required';
    return s.phase;
}
const stateTitle = (id: string) => clusters.stateById.get(id)?.message ?? '';
const stateBad = (id: string) => {
    const p = clusters.stateById.get(id)?.phase;
    return p === 'auth-required' || p === 'unreachable';
};

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

// ---- groups: create, rename, delete, production flag ----
const managing = ref(false);
const creating = ref(false);
const newName = ref('');
const renaming = ref('');
const renameTo = ref('');
const gerr = ref('');

async function guard(fn: () => Promise<unknown>) {
    gerr.value = '';
    try {
        await fn();
        await clusters.refreshList();
    } catch (e) {
        gerr.value = errorText(e);
    }
}
function createGroup() {
    const name = newName.value.trim();
    if (!name || !clusters.checked.size) {
        gerr.value = clusters.checked.size ? 'Name the group' : 'Check at least one cluster first';
        return;
    }
    void guard(async () => {
        await settings.patch(groupPatch(name, [...clusters.checked]));
        creating.value = false;
        newName.value = '';
    });
}
function startRename(g: string) {
    renaming.value = g;
    renameTo.value = g;
}
function doRename() {
    const from = renaming.value;
    const to = renameTo.value.trim();
    renaming.value = '';
    if (!to || to === from) return;
    void guard(async () => {
        await settings.patch(renameGroupPatch(settings.groups, from, to));
        if (settings.productionGroups.includes(from))
            await settings.patch(productionGroupsPatch(settings.productionGroups.map(x => (x === from ? to : x))));
    });
}
function setMembers(g: string) {
    void guard(() => settings.patch(groupPatch(g, [...clusters.checked])));
}
function removeGroup(g: string) {
    void guard(() => settings.patch(deleteGroupPatch(g)));
}
const isProdGroup = (g: string) => settings.productionGroups.includes(g);
function toggleProd(g: string) {
    const cur = settings.productionGroups;
    void guard(() => settings.patch(productionGroupsPatch(cur.includes(g) ? cur.filter(x => x !== g) : [...cur, g])));
}

// ---- linked write contexts ----
const suggestion = computed(
    () =>
        suggestLinks(
            clusters.list.map(c => ({ id: c.id, name: c.name })),
            settings.links,
            dismissedLinks(settings.doc)
        )[0]
);
const nameOf = (id: string) => clusters.nameById.get(id) ?? id;
function link(read: string, write: string) {
    if (!write) return;
    void guard(async () => {
        await settings.patch({ links: { [read]: write } });
        clusters.setChecked([...clusters.checked].filter(id => id !== write));
    });
}
function unlink(read: string) {
    void guard(() => settings.patch({ links: { [read]: null } }));
}
function dismiss(read: string, write: string) {
    void guard(() => settings.patch(dismissLinkPatch(read, write)));
}
function setAutoSwitch(e: Event) {
    const on = (e.target as HTMLInputElement).checked;
    settings.patch({ autoSwitchWrite: on }).catch(err => ui.toast('danger', errorText(err)));
}
</script>

<template>
    <template v-if="clusters.popoverOpen">
        <div class="backdrop" @mousedown="clusters.close()"></div>
        <section class="floating panel" role="dialog" aria-label="Choose clusters">
            <div class="head">
                <div class="hl">
                    <span class="lbl">Groups</span>
                    <span class="muted small">click selects a group, shift-click adds it</span>
                    <div class="grow"></div>
                    <button v-if="groups.length" class="btn ghost sm" type="button" :aria-pressed="managing" @click="managing = !managing">
                        {{ managing ? 'Done' : 'Manage' }}
                    </button>
                </div>
                <div class="gchips">
                    <button v-for="[g, ids] in groups" :key="g" class="chip" type="button" @click="pickGroup(ids, $event)">
                        {{ g }} <span class="muted">{{ ids.length }}</span><span v-if="isProdGroup(g)" class="tag danger prodtag">prod</span>
                    </button>
                    <button v-if="!creating" class="btn ghost sm" type="button" @click="creating = true">+ New group</button>
                    <form v-else class="newg" @submit.prevent="createGroup" @keydown.esc.stop="creating = false">
                        <label class="field sm"
                            ><input v-model="newName" type="text" placeholder="Group name" aria-label="New group name" autofocus
                        /></label>
                        <button class="btn sm primary" type="submit">Create from {{ clusters.checked.size }} checked</button>
                    </form>
                </div>
                <ul v-if="managing" class="manage">
                    <li v-for="[g, ids] in groups" :key="g">
                        <template v-if="renaming === g">
                            <form class="newg" @submit.prevent="doRename" @keydown.esc.stop="renaming = ''">
                                <label class="field sm"><input v-model="renameTo" type="text" :aria-label="`Rename ${g}`" /></label>
                                <button class="btn sm" type="submit">Rename</button>
                            </form>
                        </template>
                        <template v-else>
                            <span class="gn">{{ g }} <span class="muted">{{ ids.length }}</span></span>
                            <button class="btn ghost sm" type="button" @click="startRename(g)">Rename</button>
                            <button
                                class="btn ghost sm"
                                type="button"
                                title="Replace the group's members with the checked clusters"
                                @click="setMembers(g)"
                            >
                                Set to checked
                            </button>
                            <label class="prodflag"
                                ><input class="cb" type="checkbox" :checked="isProdGroup(g)" @change="toggleProd(g)" />Production</label
                            >
                            <button class="btn ghost sm danger" type="button" :aria-label="`Delete group ${g}`" @click="removeGroup(g)">
                                Delete
                            </button>
                        </template>
                    </li>
                </ul>
                <p v-if="gerr" class="err" role="alert">{{ gerr }}</p>
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
                <li v-for="c in visible" :key="c.id" class="lrow">
                    <label class="crow" :class="{ on: clusters.checked.has(c.id) }">
                        <input
                            class="cb"
                            type="checkbox"
                            :checked="clusters.checked.has(c.id)"
                            :aria-label="`Select ${c.name}`"
                            @change="clusters.toggle(c.id)"
                        />
                        <span class="dot" :style="{ background: phaseColor(clusters.stateById.get(c.id)?.phase ?? '') }"></span>
                        <span class="ctext">
                            <span class="cname">{{ c.name }}<span v-for="g in c.groups" :key="g" class="tag">{{ g }}</span></span>
                            <span class="muted small sub"
                                ><span class="mono">{{ c.server }}</span
                                ><template v-if="c.writeContext"
                                    ><span>·</span><span class="tag warning">write</span><span class="mono">{{ c.writeContext }}</span></template
                                ></span
                            >
                        </span>
                        <span class="small state" :class="{ bad: stateBad(c.id) }" :title="stateTitle(c.id)">{{ stateText(c.id) }}</span>
                    </label>
                    <p v-if="stateBad(c.id) && stateTitle(c.id)" class="small smsg">{{ stateTitle(c.id) }}</p>
                    <div class="linkrow small">
                        <template v-if="c.writeContext">
                            <button class="btn ghost sm" type="button" :aria-label="`Unlink write context of ${c.name}`" @click="unlink(c.id)">
                                Unlink write context
                            </button>
                        </template>
                        <label v-else class="linksel">
                            <span class="sr-only">Link a write context for {{ c.name }}</span>
                            <select
                                :aria-label="`Link a write context for ${c.name}`"
                                @change="link(c.id, ($event.target as HTMLSelectElement).value)"
                            >
                                <option value="">Link write context…</option>
                                <option v-for="o in clusters.list.filter(x => x.id !== c.id)" :key="o.id" :value="o.id">{{ o.name }}</option>
                            </select>
                        </label>
                    </div>
                </li>
                <li v-if="!visible.length" class="empty muted">No matching contexts</li>
            </ul>
            <div v-if="suggestion" class="suggest" role="note">
                <svg
                    width="14"
                    height="14"
                    viewBox="0 0 16 16"
                    fill="none"
                    stroke="var(--info)"
                    stroke-width="1.6"
                    aria-hidden="true"
                    style="flex: none"
                >
                    <path d="M6.5 9.5l3-3" />
                    <path d="M7 4.5l1-1a2.5 2.5 0 013.5 3.5l-1 1" />
                    <path d="M9 11.5l-1 1a2.5 2.5 0 01-3.5-3.5l1-1" />
                </svg>
                <span class="grow"
                    ><span class="mono">{{ nameOf(suggestion.read) }}</span> and <span class="mono">{{ nameOf(suggestion.write) }}</span> look like
                    one cluster. Link them as read and write?</span
                >
                <button class="btn sm" type="button" @click="link(suggestion.read, suggestion.write)">Link</button>
                <button class="btn ghost sm" type="button" @click="dismiss(suggestion.read, suggestion.write)">Dismiss</button>
            </div>
            <label class="auto small"
                ><input class="cb" type="checkbox" :checked="settings.autoSwitchWrite" @change="setAutoSwitch" />Switch to the linked write context
                automatically when the read context is denied</label
            >
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
    overflow: auto;
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
    color: var(--text3);
}
.state.bad {
    color: var(--danger);
}
.sub {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
}
.lrow {
    display: flex;
    flex-direction: column;
}
.smsg {
    margin: -2px 12px 4px 48px;
    color: var(--danger);
}
.linkrow {
    padding: 0 12px 4px 46px;
    display: none;
}
.lrow:hover .linkrow,
.lrow:focus-within .linkrow {
    display: block;
}
.linksel select {
    font-family: inherit;
    font-size: 12px;
    height: 24px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
    color: var(--text2);
    max-width: 220px;
}
.prodtag {
    height: 15px;
    font-size: 10px;
    padding: 0 4px;
    margin-left: 4px;
}
.manage {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
}
.manage li {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
}
.gn {
    flex-grow: 1;
    font-weight: 500;
}
.prodflag {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;
    color: var(--text2);
}
.newg {
    display: inline-flex;
    gap: 6px;
    align-items: center;
}
.field.sm {
    height: 26px;
    border-radius: 5px;
    width: 160px;
}
.suggest {
    margin: 4px 12px 6px;
    padding: 10px 12px;
    border-radius: 8px;
    background: var(--info-soft);
    border: 1px solid var(--info-b);
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 12px;
    color: var(--text2);
}
.auto {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 14px 8px;
    color: var(--text2);
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
