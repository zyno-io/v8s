import type { FluxGraphResult } from '@v8s/client';

import type { Layout } from './flux-layout.ts';

import { layoutFluxGraph } from './flux-layout.ts';

export class StaleLayout extends Error {
    constructor() {
        super('layout superseded');
        this.name = 'StaleLayout';
    }
}

let worker: Worker | null = null;
let seq = 0;
const pending = new Map<number, { resolve: (l: Layout) => void; reject: (e: Error) => void }>();

function getWorker(): Worker {
    if (worker) return worker;
    const w = new Worker(new URL('../workers/flux-layout.worker.ts', import.meta.url), { type: 'module' });
    w.onmessage = (ev: MessageEvent<{ id: number; layout?: Layout; error?: string }>) => {
        const p = pending.get(ev.data.id);
        if (!p) return; // cancelled
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

/**
 * Lays the graph out in a worker (inline when `Worker` is unavailable). A newer call supersedes older pending ones:
 * their promises reject with `StaleLayout`, and their results are dropped.
 */
export function layoutGraph(graph: FluxGraphResult): Promise<Layout> {
    for (const p of pending.values()) p.reject(new StaleLayout());
    pending.clear();
    if (typeof Worker === 'undefined') return Promise.resolve().then(() => layoutFluxGraph(graph));
    const id = ++seq;
    return new Promise<Layout>((resolve, reject) => {
        pending.set(id, { resolve, reject });
        try {
            getWorker().postMessage({ id, graph });
        } catch (e) {
            pending.delete(id);
            reject(e instanceof Error ? e : new Error(String(e)));
        }
    });
}
