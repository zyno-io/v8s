<script setup lang="ts">
import type { AccessResult, ObjectRef } from '@v8s/client';

import { computed, ref, watch } from 'vue';

import type { ActionDef } from '@/lib/actions.ts';

import { checkAccess } from '@/lib/access.ts';
import { accessCheckFor, actionsFor, actionState, confirmPhrase, needsTypeToConfirm } from '@/lib/actions.ts';
import { runAction, runDelete } from '@/lib/mutations.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{
    gvr: string;
    targets: ObjectRef[];
    kindLabel: string;
    /** `bar` renders buttons; `menu` renders one button that opens a list. */
    variant: 'bar' | 'menu';
    currentReplicas?: number;
    currentRevision?: number;
}>();
const emit = defineEmits<{ done: [action: string, ok: boolean] }>();

const ui = useUiStore();
const clusters = useClustersStore();
const settings = useSettingsStore();

const actions = computed(() => actionsFor(props.gvr).filter(a => props.targets.length === 1 || a.bulk));

// Access: one check per action and distinct (cluster, namespace[, name]); a bulk selection checks at namespace level.
const results = ref<Map<string, AccessResult[]>>(new Map());
let seq = 0;
async function refreshAccess() {
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
    () => [props.gvr, props.targets],
    () => {
        clearTimeout(t);
        results.value = new Map();
        if (props.targets.length) t = setTimeout(refreshAccess, 120);
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
        return ctxs.length ? `Runs as ${ctxs.join(', ')}` : st.reason;
    }
    return '';
}
const viaWrite = (a: ActionDef) => stateOf(a).viaWrite;

const open = ref(false);

const names = (n: number) => `${n} ${props.kindLabel.toLowerCase()}`;

function finish(action: string, ok: boolean) {
    emit('done', action, ok);
}

function go(a: ActionDef) {
    open.value = false;
    const targets = props.targets;
    const first = targets[0];
    if (!first) return;
    const clusterIds = [...new Set(targets.map(x => x.cluster))];
    const prod = clusterIds.some(c => clusters.isProduction(c));
    if (a.id === 'scale') {
        ui.dialog = {
            type: 'scale',
            targets,
            current: props.currentReplicas ?? 1,
            run: async n => finish(a.id, await runAction(targets, 'scale', 'Scaled', { replicas: n }))
        };
        return;
    }
    if (a.id === 'rollback') {
        ui.dialog = {
            type: 'rollback',
            target: first,
            current: props.currentRevision ?? 0,
            run: async rev => finish(a.id, await runAction(targets, 'rollback', 'Rolled back', { revision: rev }))
        };
        return;
    }
    if (a.id === 'delete') {
        const typed = needsTypeToConfirm({
            count: targets.length,
            clusters: clusterIds,
            groupsOf: clusters.groupsOf,
            productionGroups: settings.productionGroups
        });
        const where =
            clusterIds.length > 1
                ? ` across ${clusterIds.length} clusters (${clusterIds.map(c => clusters.nameById.get(c) ?? c).join(', ')})`
                : ` on ${clusters.nameById.get(first.cluster) ?? first.cluster}`;
        ui.dialog = {
            type: 'confirm',
            title: targets.length === 1 ? `Delete ${props.kindLabel.toLowerCase()} ${first.name}?` : `Delete ${names(targets.length)}?`,
            body:
                targets.length === 1
                    ? `${first.namespace ? first.namespace + '/' : ''}${first.name}${where}. This cannot be undone.`
                    : `${targets.length} objects${where}. This cannot be undone.`,
            confirmLabel: 'Delete',
            danger: true,
            phrase: typed ? confirmPhrase(targets.length, first.name ?? '') : undefined,
            note: prod ? 'This cluster is in a production group.' : undefined,
            run: async () => finish(a.id, await runDelete(targets))
        };
        return;
    }
    const doIt = async () => finish(a.id, await runAction(targets, a.id, pastTense(a), undefined));
    if (targets.length > 1 || prod) {
        ui.dialog = {
            type: 'confirm',
            title: `${a.label.replace('…', '')} ${targets.length === 1 ? first.name : names(targets.length)}?`,
            body: prod ? 'This cluster is in a production group.' : `${targets.length} objects will be affected.`,
            confirmLabel: a.label.replace('…', ''),
            danger: false,
            run: doIt
        };
        return;
    }
    void doIt();
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
            :disabled="stateOf(a).disabled"
            :title="tip(a)"
            :data-action="a.id"
            @click="go(a)"
        >
            {{ a.label }}<span v-if="viaWrite(a)" class="tag warning wtag">write</span>
        </button>
    </div>
    <div v-else class="amenu" @keydown.esc.stop="open && (open = false)">
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
                    :disabled="stateOf(a).disabled"
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
