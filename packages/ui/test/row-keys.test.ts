import type { Kind, ObjectRef } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { ownerRef, rowIntent } from '../src/lib/row-keys.ts';

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
