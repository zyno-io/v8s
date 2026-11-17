import type { HubbleServiceMap } from '@v8s/client';

import { layoutServiceMap } from '../lib/servicemap-layout.ts';

/** Off-thread service map layout: {id, graph} in, {id, layout} (or {id, error}) out. */
self.onmessage = (ev: MessageEvent<{ id: number; graph: Pick<HubbleServiceMap, 'nodes' | 'edges'> }>) => {
    const { id, graph } = ev.data;
    const post = (self as unknown as { postMessage(m: unknown): void }).postMessage;
    try {
        post.call(self, { id, layout: layoutServiceMap(graph) });
    } catch (e) {
        post.call(self, { id, error: e instanceof Error ? e.message : String(e) });
    }
};
