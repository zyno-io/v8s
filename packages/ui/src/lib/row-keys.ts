import type { Kind, ObjectOwner, ObjectRef } from '@v8s/client';

import { shallowRef } from 'vue';

import { resolveKind } from './kinds.ts';

/** What a k9s-style row key asks for. */
export type RowIntent = 'delete' | 'owner' | 'yaml' | 'copy-name' | 'copy-namespace' | 'node' | 'portforward' | 'restart';

/**
 * Maps a keydown to a row intent (null = not one of these keys). Plain and Shift keys only, except Ctrl+D (k9s "Delete");
 * Cmd+D stays the browser's bookmark and Ctrl+K stays search.
 */
export function rowIntent(e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'>): RowIntent | null {
    if (e.metaKey || e.altKey) return null;
    if (e.ctrlKey) return !e.shiftKey && e.key === 'd' ? 'delete' : null;
    // The key value carries the shift (`J`, `F`); only the capital letters count, not Shift with another key.
    if (e.key === 'J') return 'owner';
    if (e.key === 'F') return 'portforward';
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
        const k = resolveKind(kinds, o.kind);
        if (k) return { cluster: ref.cluster, gvr: k.gvr, namespace: k.namespaced ? ref.namespace : undefined, name: o.name };
    }
    return null;
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
export function requestKeyAction(gvr: string, action: string, targets: ObjectRef[]) {
    keyAction.value = { seq: ++seq, gvr, action, targets };
}
