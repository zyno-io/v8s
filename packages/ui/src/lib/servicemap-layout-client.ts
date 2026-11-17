import type { HubbleServiceMap } from '@v8s/client';

import type { MapLayout } from './servicemap-layout.ts';

import { layoutServiceMap } from './servicemap-layout.ts';

export class StaleLayout extends Error {
    constructor() {
        super('layout superseded');
        this.name = 'StaleLayout';
    }
}

type Graph = Pick<HubbleServiceMap, 'nodes' | 'edges'>;

let worker: Worker | null = null;
let seq = 0;
const pending = new Map<number, { resolve: (l: MapLayout) => void; reject: (e: Error) => void }>();

function getWorker(): Worker {
    if (worker) return worker;
    const w = new Worker(new URL('../workers/servicemap-layout.worker.ts', import.meta.url), { type: 'module' });
    w.onmessage = (ev: MessageEvent<{ id: number; layout?: MapLayout; error?: string }>) => {
        const p = pending.get(ev.data.id);
        if (!p) return;
        pending.delete(ev.data.id);
        if (ev.data.layout) p.resolve(ev.data.layout);
        else p.reject(new Error(ev.data.error ?? 'layout failed'));
    };
    w.onerror = () => {
        for (const p of pending.values()) p.reject(new Error('layout worker failed'));
        pending.clear();
        w.terminate();
        if (worker === w) worker = null;
    };
    worker = w;
    return w;
}

/** Lays the map out in a worker (inline when `Worker` is unavailable). A newer call supersedes older pending ones. */
export function layoutMap(graph: Graph): Promise<MapLayout> {
    for (const p of pending.values()) p.reject(new StaleLayout());
    pending.clear();
    if (typeof Worker === 'undefined') return Promise.resolve().then(() => layoutServiceMap(graph));
    const id = ++seq;
    return new Promise<MapLayout>((resolve, reject) => {
        pending.set(id, { resolve, reject });
        try {
            getWorker().postMessage({ id, graph });
        } catch (e) {
            pending.delete(id);
            reject(e instanceof Error ? e : new Error(String(e)));
        }
    });
}
