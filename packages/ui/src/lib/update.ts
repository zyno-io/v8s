import type { Agent, Settings, UpdateStatus } from '@v8s/client';

import { shallowRef, watch } from 'vue';

/** The newer stable release the agent found, if any. The agent checks v8s.io/release.json; the UI only asks it. */
export const updateStatus = shallowRef<UpdateStatus | null>(null);

const POLL_MS = 5 * 60 * 1000;

/** "v8s 0.4.0" for tooltips and menus; just "v8s" until the agent has said its version. */
export function versionTitle(v: string | null | undefined): string {
    const n = (v ?? '').replace(/^v/, '');
    return n ? `v8s ${n}` : 'v8s';
}

/** The version whose pill the user dismissed (`uiPrefs.updateDismissed`); anything not a string reads as none. */
export function dismissedUpdate(ui: Settings['uiPrefs'] | null | undefined): string {
    const v = ui?.updateDismissed;
    return typeof v === 'string' ? v : '';
}

/** A settings merge patch that dismisses the pill for one version. */
export function dismissPatch(v: string): Record<string, unknown> {
    return { uiPrefs: { updateDismissed: v } };
}

/** The status to show as a pill: set, and not the dismissed version. A newer release brings it back. */
export function pendingUpdate(s: UpdateStatus | null | undefined, dismissed: string): UpdateStatus | null {
    return s?.latest && s.latest !== dismissed ? s : null;
}

/**
 * Keeps `updateStatus` current: asks now, after every reconnect (a restarted agent may know more) and every 5 minutes (the agent
 * caches its own v8s.io answer, so this is a local call). Errors are swallowed; the ref only changes when the answer does.
 * Returns a stop function.
 */
export function watchUpdates(agent: Pick<Agent, 'call' | 'reconnects'>): () => void {
    const poll = async () => {
        try {
            // An empty status may arrive as no result at all.
            const s = (await agent.call<UpdateStatus | null | undefined>('update.status')) ?? {};
            const cur = updateStatus.value;
            const next = s.latest ? { latest: s.latest, url: s.url } : null;
            if ((cur?.latest ?? '') !== (next?.latest ?? '') || (cur?.url ?? '') !== (next?.url ?? '')) updateStatus.value = next;
        } catch {
            // the indicator is a convenience; try again next time
        }
    };
    void poll();
    const stopWatch = watch(agent.reconnects, () => void poll());
    const timer = setInterval(() => void poll(), POLL_MS);
    return () => {
        stopWatch();
        clearInterval(timer);
    };
}
