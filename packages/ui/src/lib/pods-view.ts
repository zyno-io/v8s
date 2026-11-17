import type { ViewHandle } from '@v8s/client';

import { onScopeDispose, watch } from 'vue';

import { buildPodsQuery, TONE_CHIPS, usePodsStore } from '@/stores/pods.ts';

import { getAgent } from './agent.ts';
import { windowPad } from './viewport.ts';

/**
 * Subscribes the pods list view plus four one-row count views (one per tone chip). Count views never render rows; they only
 * contribute `total`, so the chips show live counts without the UI computing anything over the dataset.
 */
export function usePodsView() {
    const agent = getAgent();
    const pods = usePodsStore();
    const initialLimit = () => windowPad() * 2 + 60;

    const handle = agent.subscribeView(pods.query, { offset: 0, limit: initialLimit() });
    const counts: Record<string, ViewHandle> = {};
    for (const c of TONE_CHIPS) {
        counts[c.key] = agent.subscribeView(buildPodsQuery(pods.text, pods.namespaces, [c.tone], []), { offset: 0, limit: 1 });
    }

    watch(
        () => pods.query,
        q => handle.update({ query: q, window: { offset: 0, limit: initialLimit() } })
    );

    let timer: ReturnType<typeof setTimeout> | undefined;
    watch(
        () => [pods.text, pods.namespaces] as const,
        ([text, ns]) => {
            clearTimeout(timer);
            timer = setTimeout(() => {
                for (const c of TONE_CHIPS) counts[c.key].update({ query: buildPodsQuery(text, ns, [c.tone], []) });
            }, 150);
        }
    );

    onScopeDispose(() => {
        clearTimeout(timer);
        handle.close();
        for (const c of Object.values(counts)) c.close();
    });

    return { handle, counts };
}
