<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';

import LinkedContextsDialog from '@/components/LinkedContextsDialog.vue';
import LockIcon from '@/components/LockIcon.vue';
import ProtectedMark from '@/components/ProtectedMark.vue';
import { phaseColor } from '@/lib/colors.ts';
import { formatCount } from '@/lib/format.ts';
import { suggestLinks } from '@/lib/link-suggest.ts';
import { errorText } from '@/lib/mutations.ts';
import { renameStoredGroup } from '@/lib/remember.ts';
import { deleteGroupPatch, dismissedLinks, groupList, groupPatch, renameGroupPatch } from '@/lib/settings.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';

const clusters = useClustersStore();
const settings = useSettingsStore();
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
// Groups the server puts clusters in (hosted mode's displayGroups): selectable like any group, but read-only here.
const fixedGroups = computed(() => {
    const m = new Map<string, string[]>();
    for (const c of clusters.list) for (const g of c.fixedGroups ?? []) m.set(g, [...(m.get(g) ?? []), c.id]);
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
});
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
        if (!open) linkedOpen.value = false;
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

// ---- groups: create, rename, delete ----
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
    // Its remembered namespace filters follow the new name (also when it is not connected now).
    renameStoredGroup(from, to);
    void guard(() => settings.patch(renameGroupPatch(settings.groups, from, to)));
}
function setMembers(g: string) {
    void guard(() => settings.patch(groupPatch(g, [...clusters.checked])));
}
function removeGroup(g: string) {
    void guard(() => settings.patch(deleteGroupPatch(g)));
}

// ---- protected clusters: a lock toggle at the end of every row ----
const isProt = (id: string) => settings.protectedClusters.includes(id);
function toggleProtect(id: string) {
    void guard(() => settings.toggleProtect(id));
}

// ---- linked write contexts (managed in LinkedContextsDialog) ----
const linkedOpen = ref(false);
const linkCount = computed(() => Object.keys(settings.links).length);
const suggestions = computed(() =>
    suggestLinks(
        clusters.list.map(c => ({ id: c.id, name: c.name })),
        settings.links,
        dismissedLinks(settings.doc)
    )
);
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
                    <button
                        v-for="[g, ids] in fixedGroups"
                        :key="'fixed:' + g"
                        class="chip fixed"
                        type="button"
                        title="Set by the server; read-only"
                        data-testid="fixed-group"
                        @click="pickGroup(ids, $event)"
                    >
                        {{ g }} <span class="muted">{{ ids.length }}</span>
                    </button>
                    <button v-for="[g, ids] in groups" :key="g" class="chip" type="button" @click="pickGroup(ids, $event)">
                        {{ g }} <span class="muted">{{ ids.length }}</span>
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
                            <button class="btn ghost sm danger" type="button" :aria-label="`Delete group ${g}`" @click="removeGroup(g)">
                                Delete
                            </button>
                        </template>
                    </li>
                </ul>
                <p v-if="managing && fixedGroups.length" class="muted small fixnote">
                    Groups with a dashed border are set by the server and can't be changed here.
                </p>
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
                    <div class="rowwrap">
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
                                <span class="cname"
                                    >{{ c.name }}<ProtectedMark :cluster="c.id" /><span v-for="g in c.groups" :key="g" class="tag">{{
                                        g
                                    }}</span></span
                                >
                                <span class="muted small sub"><span class="mono url">{{ c.server }}</span></span>
                                <span v-if="c.writeContext" class="muted small sub"
                                    ><span class="tag warning">write</span><span class="mono">{{ c.writeContext }}</span></span
                                >
                            </span>
                            <span class="small state" :class="{ bad: stateBad(c.id) }" :title="stateTitle(c.id)">{{ stateText(c.id) }}</span>
                        </label>
                        <button
                            class="lock"
                            :class="{ on: isProt(c.id) }"
                            type="button"
                            :aria-pressed="isProt(c.id)"
                            :aria-label="`Protect ${c.name}`"
                            :title="
                                isProt(c.id)
                                    ? 'Protected: destructive actions ask you to type the name to confirm. Click to unprotect.'
                                    : 'Destructive actions on a protected cluster ask you to type the name to confirm.'
                            "
                            data-testid="protect-toggle"
                            @click="toggleProtect(c.id)"
                        >
                            <LockIcon :size="14" :filled="isProt(c.id)" />
                        </button>
                    </div>
                    <p v-if="stateBad(c.id) && stateTitle(c.id)" class="small smsg">{{ stateTitle(c.id) }}</p>
                </li>
                <li v-if="!visible.length" class="empty muted">No matching contexts</li>
            </ul>
            <div v-if="suggestions.length" class="suggest" role="note">
                <span class="grow"
                    >{{ suggestions.length }} context {{ suggestions.length === 1 ? 'pair looks' : 'pairs look' }} like read/write pairs.</span
                >
                <button class="btn sm" type="button" @click="linkedOpen = true">Review</button>
            </div>
            <p v-if="clusters.error" class="err" role="alert">{{ clusters.error }}</p>
            <div class="foot">
                <span class="small note">{{ note }}</span>
                <button class="btn ghost sm" type="button" aria-haspopup="dialog" data-testid="linked-open" @click="linkedOpen = true">
                    Linked contexts…{{ linkCount ? ` (${linkCount})` : '' }}
                </button>
                <div class="grow"></div>
                <button class="btn" type="button" @click="clusters.close()">Cancel</button>
                <button class="btn primary" type="button" :disabled="clusters.connecting" @click="connect">
                    {{ clusters.connecting ? 'Connecting…' : 'Connect' }}
                </button>
            </div>
        </section>
        <LinkedContextsDialog v-if="linkedOpen" @close="linkedOpen = false" />
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
    width: min(580px, calc(100vw - 16px));
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
.chip.fixed {
    border-style: dashed;
}
.fixnote {
    margin: 0;
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
.sub + .sub {
    margin-top: 2px;
}
.url {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.lrow {
    display: flex;
    flex-direction: column;
}
.smsg {
    margin: -2px 12px 4px 48px;
    color: var(--danger);
}
.rowwrap {
    display: flex;
    align-items: center;
    gap: 2px;
}
.rowwrap .crow {
    flex: 1;
    min-width: 0;
}
.lock {
    flex: none;
    width: 28px;
    height: 28px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text3);
    cursor: pointer;
}
.lock:hover {
    background: var(--hover);
    color: var(--text2);
}
.lock.on,
.lock.on:hover {
    color: var(--danger);
}
@media (pointer: coarse) {
    .lock {
        width: 44px;
        height: 44px;
    }
}
.lock:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
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
    flex-wrap: wrap;
}
.note {
    color: var(--text2);
    white-space: nowrap;
}
.foot .btn {
    white-space: nowrap;
}
@media (max-width: 639.98px) {
    .panel {
        left: 8px;
        top: 52px;
    }
}
</style>
