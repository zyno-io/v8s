<script setup lang="ts">
import type { AccessResult, ObjectRef } from '@v8s/client';

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import type { ActionDef } from '@/lib/actions.ts';

import { checkAccess } from '@/lib/access.ts';
import { accessCheckFor, actionsFor, actionState, confirmPhrase, isBulkOrCross, needsTypeToConfirm, targetSummary } from '@/lib/actions.ts';
import { fluxActionDefs, runFluxAction } from '@/lib/flux-actions.ts';
import { isFluxGvr } from '@/lib/flux.ts';
import { drainRequest } from '@/lib/jobs.ts';
import { runAction, runAs, runDelete } from '@/lib/mutations.ts';
import { nodeShellAvailable, openNodeShell } from '@/lib/node-shell.ts';
import { portForwardAvailable, portForwardRequest } from '@/lib/portforward.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{
    gvr: string;
    targets: ObjectRef[];
    kindLabel: string;
    /** `bar` renders buttons; `menu` renders one button that opens a list; `key` renders nothing and runs `run`. */
    variant: 'bar' | 'menu' | 'key';
    /** variant `key`: the action id to run on mount (after its access check), through the same confirmations as a click. */
    run?: string;
    currentReplicas?: number;
    currentRevision?: number;
}>();
const emit = defineEmits<{
    done: [action: string, ok: boolean];
    /** variant `key`: the request ended without running (declined, denied, or its dialog was cancelled). */
    end: [];
}>();

const ui = useUiStore();
const clusters = useClustersStore();

const actions = computed(() => {
    const base = isFluxGvr(props.gvr) ? [...fluxActionDefs(props.gvr), ...actionsFor(props.gvr)] : actionsFor(props.gvr);
    return base.filter(a => {
        if (props.targets.length !== 1 && !a.bulk) return false;
        if (a.client === 'nodeshell') return nodeShellAvailable();
        if (a.client === 'portforward') return portForwardAvailable();
        return true;
    });
});

// Access: one check per action and distinct (cluster, namespace[, name]); a bulk selection checks at namespace level.
const results = ref<Map<string, AccessResult[]>>(new Map());
let seq = 0;
async function refreshAccess(only?: string) {
    const my = ++seq;
    const single = props.targets.length === 1;
    const seen = new Map<string, ObjectRef>();
    for (const t of props.targets) {
        const k = single ? `${t.cluster}|${t.namespace}|${t.name}` : `${t.cluster}|${t.namespace}`;
        if (!seen.has(k)) seen.set(k, single ? t : { ...t, name: '' });
        if (seen.size >= 20) break;
    }
    const refs = [...seen.values()];
    const out = new Map<string, AccessResult[]>();
    try {
        for (const a of actions.value) {
            if (a.noCheck || (only && a.id !== only)) continue;
            out.set(a.id, await checkAccess(refs.map(r => accessCheckFor(a, r))));
            if (my !== seq) return;
        }
    } catch {
        // an access-check failure leaves the buttons enabled; the server decides
    }
    if (my === seq) results.value = out;
}
let t: ReturnType<typeof setTimeout> | undefined;
watch(
    () => [props.gvr, props.targets, clusters.elevatedKey],
    () => {
        clearTimeout(t);
        results.value = new Map();
        if (props.targets.length && !props.run) t = setTimeout(refreshAccess, 120);
    },
    { immediate: true }
);

function stateOf(a: ActionDef) {
    return actionState(results.value.get(a.id) ?? []);
}
function tip(a: ActionDef): string {
    const st = stateOf(a);
    if (st.disabled) return st.reason;
    if (st.viaWrite) {
        const ctxs = [...new Set(props.targets.map(x => clusters.writeContextOf(x.cluster)).filter(Boolean))];
        const base = ctxs.length ? `Runs as ${ctxs.join(', ')}` : '';
        // The agent's note (e.g. the write context could not be checked: is its elevated session active?) adds to it.
        const note = st.reason && st.reason !== 'Will run as the linked write context' ? st.reason : '';
        return [base, note].filter(Boolean).join(' — ') || st.reason;
    }
    return '';
}
const viaWrite = (a: ActionDef) => stateOf(a).viaWrite;

// variant `key`: one-shot. Checks access for the one action, then opens its confirmation unless the page moved on meanwhile.
let alive = true;
onBeforeUnmount(() => (alive = false));
onMounted(async () => {
    if (props.variant !== 'key' || !props.run) return;
    await refreshAccess(props.run);
    if (!alive || ui.dialog) return emit('end');
    const a = actions.value.find(x => x.id === props.run);
    if (!a) return emit('end');
    const st = stateOf(a);
    if (st.disabled) {
        ui.toast('warning', st.reason);
        emit('end');
    } else go(a);
});

const open = ref(false);
/** Esc closes an open menu and stops there; with the menu closed it passes on (it closes the drawer). */
function onMenuEsc(e: KeyboardEvent) {
    if (!open.value) return;
    e.stopPropagation();
    open.value = false;
}
/** In flight: a direct action (no dialog) must not fire twice from a double click. */
const busy = ref(false);
const names = (n: number) => `${n} ${props.kindLabel.toLowerCase()}`;

function finish(action: string, ok: boolean) {
    emit('done', action, ok);
}

const nameOf = (c: string) => clusters.displayName(c);

function go(a: ActionDef) {
    open.value = false;
    if (busy.value) return;
    const targets = props.targets;
    const first = targets[0];
    if (!first) return;
    const clusterIds = [...new Set(targets.map(x => x.cluster))];
    const clusterNames = clusterIds.map(nameOf);
    const protNames = clusterIds.filter(c => clusters.isProtected(c)).map(nameOf);
    const summary = targetSummary(targets, clusterNames);
    const runsAs = [...new Set(clusterIds.map(c => runAs(c, viaWrite(a))))].join('; ');
    const note = protNames.length ? `Protected cluster: ${protNames.join(', ')}.` : undefined;
    const confirmInput = {
        count: targets.length,
        clusters: clusterIds,
        protectedClusters: clusterIds.filter(c => clusters.isProtected(c))
    };
    if (a.client === 'drain') {
        drainRequest.value = { cluster: first.cluster, node: first.name ?? '' };
        return;
    }
    if (a.client === 'nodeshell') {
        void openNodeShell(first.cluster, first.name ?? '');
        return;
    }
    if (a.client === 'portforward') {
        portForwardRequest.value = first;
        if (props.variant === 'key') emit('end');
        return;
    }
    // A key-triggered confirm: Cancel is the default button, so a stray Enter never runs it; the end of the request is signalled.
    const viaKey = props.variant === 'key';
    const keyExtras = viaKey ? { cancelFirst: true, onCancel: () => emit('end') } : {};
    const phraseFor = (typed: boolean) => (typed ? confirmPhrase(targets.length, first.name ?? '') : undefined);

    if (a.id === 'scale') {
        ui.dialog = {
            type: 'scale',
            targets,
            // Unknown replicas stay empty and required: never pre-fill a guess.
            current: props.currentReplicas ?? null,
            phrase: phraseFor(needsTypeToConfirm(confirmInput)),
            note,
            locked: !!note,
            runAs: runsAs,
            run: async n => finish(a.id, await runAction(targets, 'scale', 'Scaled', { replicas: n }))
        };
        return;
    }
    if (a.id === 'rollback') {
        ui.dialog = {
            type: 'rollback',
            target: first,
            current: props.currentRevision ?? 0,
            phrase: phraseFor(needsTypeToConfirm(confirmInput)),
            note,
            locked: !!note,
            runAs: runsAs,
            run: async rev => finish(a.id, await runAction(targets, 'rollback', 'Rolled back', { revision: rev }))
        };
        return;
    }
    if (a.id === 'delete') {
        const typed = needsTypeToConfirm({ ...confirmInput, destructive: true, phone: isPhone.value });
        ui.dialog = {
            type: 'confirm',
            title: targets.length === 1 ? `Delete ${props.kindLabel.toLowerCase()} ${first.name}?` : `Delete ${names(targets.length)}?`,
            body: `${summary}. This cannot be undone.`,
            confirmLabel: 'Delete',
            danger: true,
            phrase: phraseFor(typed),
            note,
            locked: !!note,
            runAs: runsAs,
            ...keyExtras,
            run: async () => finish(a.id, await runDelete(targets))
        };
        return;
    }
    const doIt = async () =>
        finish(a.id, a.id.startsWith('flux.') ? await runFluxAction(targets, a.id) : await runAction(targets, a.id, pastTense(a), undefined));
    // Bulk and cross-cluster (cordon included): type the count. A single object on a protected cluster: plain confirm naming the cluster.
    const typed = isBulkOrCross(targets.length, clusterIds.length);
    // On a phone restart and trigger are never a single tap: hold to confirm.
    const hold = isPhone.value && (a.id === 'restart' || a.id === 'trigger');
    // A keyboard shortcut is easy to press by accident (k9s confirms its keys too), so a key-triggered action always asks.
    if (typed || note || hold || viaKey) {
        ui.dialog = {
            type: 'confirm',
            title: `${a.label.replace('…', '')} ${targets.length === 1 ? first.name : names(targets.length)}?`,
            body: `${summary}.`,
            confirmLabel: a.label.replace('…', ''),
            danger: false,
            hold,
            phrase: phraseFor(typed),
            note,
            locked: !!note,
            runAs: runsAs,
            ...keyExtras,
            run: doIt
        };
        return;
    }
    busy.value = true;
    void doIt().finally(() => (busy.value = false));
}
function pastTense(a: ActionDef): string {
    return (
        (
            {
                restart: 'Restarted',
                cordon: 'Cordoned',
                uncordon: 'Uncordoned',
                suspend: 'Suspended',
                resume: 'Resumed',
                trigger: 'Triggered'
            } as Record<string, string>
        )[a.id] ?? a.label
    );
}
</script>

<template>
    <div v-if="variant === 'bar'" class="abar">
        <button
            v-for="a in actions"
            :key="a.id"
            class="btn sm"
            :class="{ danger: a.danger }"
            type="button"
            :disabled="stateOf(a).disabled || busy"
            :title="tip(a)"
            :data-action="a.id"
            @click="go(a)"
        >
            {{ a.label }}<span v-if="viaWrite(a)" class="tag warning wtag">write</span>
        </button>
    </div>
    <div v-else-if="variant === 'menu'" class="amenu" @keydown.esc="onMenuEsc">
        <button class="btn sm" type="button" aria-haspopup="menu" :aria-expanded="open" data-testid="actions-menu" @click="open = !open">
            Actions
            <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <path d="M4 6l4 4 4-4" />
            </svg>
        </button>
        <template v-if="open">
            <div class="back" @mousedown="open = false"></div>
            <div class="floating list" role="menu">
                <button
                    v-for="a in actions"
                    :key="a.id"
                    class="item"
                    :class="{ danger: a.danger }"
                    type="button"
                    role="menuitem"
                    :disabled="stateOf(a).disabled || busy"
                    :title="tip(a)"
                    :data-action="a.id"
                    @click="go(a)"
                >
                    {{ a.label }}<span v-if="viaWrite(a)" class="tag warning wtag">write</span>
                    <span v-if="stateOf(a).disabled" class="why">{{ stateOf(a).reason }}</span>
                </button>
            </div>
        </template>
    </div>
</template>

<style scoped>
.abar {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    align-items: center;
}
.wtag {
    height: 15px;
    font-size: 10px;
    padding: 0 4px;
}
.amenu {
    position: relative;
}
.back {
    position: fixed;
    inset: 0;
    z-index: 60;
}
.list {
    position: absolute;
    z-index: 61;
    top: 30px;
    right: 0;
    min-width: 200px;
    padding: 4px;
    display: flex;
    flex-direction: column;
}
.item {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 30px;
    padding: 4px 10px;
    border-radius: 5px;
    font-size: 13px;
    cursor: pointer;
    flex-wrap: wrap;
}
.item:hover:not([disabled]) {
    background: var(--hover);
}
.item:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.item[disabled] {
    color: var(--text3);
    cursor: default;
}
.item.danger {
    color: var(--danger-strong);
}
.why {
    flex-basis: 100%;
    font-size: 11px;
    color: var(--text3);
}
</style>
