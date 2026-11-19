import type { Kind, ObjectRef } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { keyAction, keyActionBlocked, ownerRef, requestKeyAction, rowIntent } from '../src/lib/row-keys.ts';

const k = (key: string, extra: Record<string, boolean> = {}) => ({ key, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...extra });

describe('rowIntent', () => {
    it('maps the k9s keys', () => {
        expect(rowIntent(k('d', { ctrlKey: true }))).toBe('delete');
        expect(rowIntent(k('J', { shiftKey: true }))).toBe('owner');
        expect(rowIntent(k('F', { shiftKey: true }))).toBe('portforward');
        expect(rowIntent(k('y'))).toBe('yaml');
        expect(rowIntent(k('c'))).toBe('copy-name');
        expect(rowIntent(k('n'))).toBe('copy-namespace');
        expect(rowIntent(k('o'))).toBe('node');
        expect(rowIntent(k('r'))).toBe('restart');
    });
    it('leaves existing and browser keys alone', () => {
        for (const key of ['j', 'k', 'l', 's', 'e', 'd', 'x', 'Enter']) expect(rowIntent(k(key))).toBeNull();
        expect(rowIntent(k('d', { metaKey: true }))).toBeNull(); // Cmd+D stays the bookmark
        expect(rowIntent(k('k', { ctrlKey: true }))).toBeNull(); // search
        expect(rowIntent(k('D', { ctrlKey: true, shiftKey: true }))).toBeNull();
        expect(rowIntent(k('r', { ctrlKey: true }))).toBeNull();
        expect(rowIntent(k('j', { shiftKey: true }))).toBeNull();
        expect(rowIntent(k('y', { altKey: true }))).toBeNull();
    });
});

describe('CapsLock and Shift', () => {
    it('needs Shift for J and F', () => {
        expect(rowIntent(k('J'))).toBeNull();
        expect(rowIntent(k('F'))).toBeNull();
    });
});

describe('key actions', () => {
    it('ignores a request while one is pending', () => {
        keyAction.value = null;
        const a = { cluster: 'c', gvr: 'v1/pods', name: 'a' };
        expect(requestKeyAction('v1/pods', 'delete', [a])).toBe(true);
        expect(requestKeyAction('v1/pods', 'delete', [{ ...a, name: 'b' }])).toBe(false);
        expect((keyAction as { value: { targets: ObjectRef[] } | null }).value?.targets[0].name).toBe('a');
        keyAction.value = null;
    });
    it('blocks Delete and Restart on the virtual Helm kind only', () => {
        expect(keyActionBlocked('helm.sh/v1/releases')).toBe(true);
        expect(keyActionBlocked('apps/v1/deployments')).toBe(false);
    });
});

describe('ownerRef', () => {
    const kind = (gvr: string, name: string, namespaced: boolean) => ({ gvr, kind: name, namespaced }) as unknown as Kind;
    const kinds = [kind('apps/v1/replicasets', 'ReplicaSet', true), kind('v1/nodes', 'Node', false)];
    const pod: ObjectRef = { cluster: 'c1', gvr: 'v1/pods', namespace: 'payments', name: 'api-1' };
    it('resolves a namespaced owner in the same namespace', () => {
        expect(ownerRef(pod, [{ kind: 'ReplicaSet', name: 'api-abc' }], kinds)).toEqual({
            cluster: 'c1',
            gvr: 'apps/v1/replicasets',
            namespace: 'payments',
            name: 'api-abc'
        });
    });
    it('drops the namespace for a cluster-scoped owner', () => {
        expect(ownerRef(pod, [{ kind: 'Node', name: 'n1' }], kinds)?.namespace).toBeUndefined();
    });
    it('skips unknown kinds and returns null without an owner', () => {
        expect(
            ownerRef(
                pod,
                [
                    { kind: 'Mystery', name: 'x' },
                    { kind: 'ReplicaSet', name: 'rs' }
                ],
                kinds
            )?.name
        ).toBe('rs');
        expect(ownerRef(pod, [], kinds)).toBeNull();
        expect(ownerRef(pod, undefined, kinds)).toBeNull();
    });
});
