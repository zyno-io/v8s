import type { FluxGraphResult } from '@v8s/client';

import { layoutFluxGraph } from '../lib/flux-layout.ts';

/** Off-thread Flux graph layout: {id, graph} in, {id, layout} (or {id, error}) out. */
self.onmessage = (ev: MessageEvent<{ id: number; graph: FluxGraphResult }>) => {
    const { id, graph } = ev.data;
    try {
        (self as unknown as { postMessage(m: unknown): void }).postMessage({ id, layout: layoutFluxGraph(graph) });
    } catch (e) {
        (self as unknown as { postMessage(m: unknown): void }).postMessage({ id, error: e instanceof Error ? e.message : String(e) });
    }
};
