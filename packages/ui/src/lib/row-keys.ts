import type { Kind, ObjectOwner, ObjectRef } from '@v8s/client';

import { shallowRef } from 'vue';

import { resolveKind } from './kinds.ts';
import { HELM_RELEASES_GVR } from './route.ts';

/** What a k9s-style row key asks for. */
export type RowIntent = 'delete' | 'owner' | 'yaml' | 'copy-name' | 'copy-namespace' | 'node' | 'portforward' | 'restart';

/**
 * Maps a keydown to a row intent (null = not one of these keys). Plain and Shift keys only, except Ctrl+D (k9s "Delete");
 * Cmd+D stays the browser's bookmark and Ctrl+K stays search.
 */
export function rowIntent(e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'>): RowIntent | null {
    if (e.metaKey || e.altKey) return null;
    if (e.ctrlKey) return !e.shiftKey && e.key === 'd' ? 'delete' : null;
    // Shift+J / Shift+F; CapsLock alone (a capital without Shift) does not count.
    if (e.shiftKey) return e.key === 'J' ? 'owner' : e.key === 'F' ? 'portforward' : null;
    switch (e.key) {
        case 'y':
            return 'yaml';
        case 'c':
            return 'copy-name';
        case 'n':
            return 'copy-namespace';
        case 'o':
            return 'node';
        case 'r':
            return 'restart';
    }
    return null;
}

/**
 * The object that controls `ref`: the first owner whose kind the clusters serve, in the same namespace (cluster-scoped owners
 * carry none). Null when there is none.
 */
export function ownerRef(ref: ObjectRef, owners: readonly ObjectOwner[] | undefined, kinds: readonly Kind[]): ObjectRef | null {
    for (const o of owners ?? []) {
        const t = ownerTarget(ref, o, kinds);
        if (t) return t;
    }
    return null;
}

/** One owner of `ref` as an object to open: same cluster, same namespace unless the owner's kind is cluster-scoped. Null when the clusters don't serve its kind. */
export function ownerTarget(ref: ObjectRef, o: ObjectOwner, kinds: readonly Kind[]): ObjectRef | null {
    const k = resolveKind(kinds, o.kind);
    return k ? { cluster: ref.cluster, gvr: k.gvr, namespace: k.namespaced ? ref.namespace : undefined, name: o.name } : null;
}

/** A row key that runs an existing action (with its access check and confirmation), handled by the page's hidden ActionBar. */
export interface KeyAction {
    seq: number;
    gvr: string;
    action: string;
    targets: ObjectRef[];
}
export const keyAction = shallowRef<KeyAction | null>(null);
let seq = 0;
/** Queues a request; ignored (false) while another is pending, so a second key never replaces a dialog being prepared or shown. */
export function requestKeyAction(gvr: string, action: string, targets: ObjectRef[]): boolean {
    if (keyAction.value) return false;
    keyAction.value = { seq: ++seq, gvr, action, targets };
    return true;
}

/** The virtual Helm releases kind has its own actions (Uninstall, Upgrade, ...) in its drawer; Delete and Restart do not apply. */
export function keyActionBlocked(gvr: string): boolean {
    return gvr === HELM_RELEASES_GVR;
}
