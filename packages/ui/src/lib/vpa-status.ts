import type { Column, Row, ViewHandle } from '@v8s/client';
import type { Ref } from 'vue';

import { TONE_NAMES } from '@v8s/client';
import { onBeforeUnmount, shallowRef, watch } from 'vue';

import type { Tone } from './table-decor.ts';

import { getAgent } from './agent.ts';

/** One VPA's status as its list row shows it (Provided, LowConfidence, NoPodsMatched, ConfigUnsupported, Pending) and its tone. */
export interface VpaState {
    status: string;
    tone: Tone;
}
export type VpaIndex = ReadonlyMap<string, VpaState>;

export const vpaKey = (cluster: string, namespace: string, name: string): string => cluster + '\0' + namespace + '\0' + name;

const EMPTY: VpaIndex = new Map();

/** The `want` key of useVpaIndex: a string, so an unchanged subscription compares equal. JSON: cluster ids may hold any character. */
export const vpaWantKey = (gvr: string, clusters: readonly string[]): string => JSON.stringify([gvr, clusters]);

/** Builds the index from a VPA view's rows: cluster/namespace/name → status and tone. */
export function buildVpaIndex(columns: readonly Column[] | null, ids: ArrayLike<number>, rows: ReadonlyMap<number, Row>): VpaIndex {
    if (!columns) return EMPTY;
    const at = (key: string) => {
        const i = columns.findIndex(c => c.key === key);
        return i < 0 ? -1 : i + 2;
    };
    const cl = at('cluster');
    const ns = at('namespace');
    const nm = at('name');
    const st = at('status');
    const tn = at('statusTone');
    if (cl < 0 || ns < 0 || nm < 0 || tn < 0) return EMPTY;
    const out = new Map<string, VpaState>();
    for (let i = 0; i < ids.length; i++) {
        const r = rows.get(ids[i]);
        if (!r) continue;
        const tone = (TONE_NAMES[r[tn] as number] ?? 'neutral') as Tone;
        out.set(vpaKey(r[cl] as string, r[ns] as string, r[nm] as string), { status: st >= 0 ? String(r[st] ?? '') : '', tone });
    }
    return out;
}

/** Same keys with the same status and tone (recommendation values may differ: they do not affect the pods table). */
export function sameVpaIndex(a: VpaIndex, b: VpaIndex): boolean {
    if (a.size !== b.size) return false;
    for (const [k, v] of a) {
        const w = b.get(k);
        if (!w || w.status !== v.status || w.tone !== v.tone) return false;
    }
    return true;
}

/**
 * Subscribes to the VPA list while `want` is non-empty (a vpaWantKey) and exposes its status index. The pods table colours
 * its VPA tags from it. The index object is replaced only when a status or tone changed, so recommendation updates (every recommender
 * loop) re-render nothing. VPAs are few; the agent watches them only while this subscription is open. It is a background
 * subscription: where the user may not list VPAs, the refused watch is parked instead of retried for as long as Pods is open.
 */
export function useVpaIndex(want: Ref<string>): Ref<VpaIndex> {
    const index = shallowRef<VpaIndex>(EMPTY);
    let h: ViewHandle | null = null;
    let stop: (() => void) | null = null;
    const close = () => {
        stop?.();
        stop = null;
        h?.close();
        h = null;
        index.value = EMPTY;
    };
    watch(
        want,
        w => {
            close();
            if (!w) return;
            const [gvr, clusters] = JSON.parse(w) as [string, string[]];
            const handle = getAgent().subscribeView(
                { clusters, gvr, namespaces: [], text: '', filters: [], sort: [], background: true },
                { offset: 0, limit: 20000 }
            );
            h = handle;
            stop = watch(handle.version, () => {
                const next = buildVpaIndex(handle.columns.value, handle.ids, handle.rows);
                if (!sameVpaIndex(index.value, next)) index.value = next;
            });
        },
        { immediate: true }
    );
    onBeforeUnmount(close);
    return index;
}
