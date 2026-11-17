import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it } from 'vitest';

import { selectionKey, useTableStore } from '../src/stores/table.ts';

beforeEach(() => setActivePinia(createPinia()));

describe('selection', () => {
    const a = { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'web-1' };
    const b = { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'web-10' };

    it('is keyed by object identity, not by row id', () => {
        expect(selectionKey(a)).not.toBe(selectionKey(b));
        const t = useTableStore();
        t.select('v1/pods', a);
        expect(t.of('v1/pods').selected.has(selectionKey(a))).toBe(true);
        t.select('v1/pods', a);
        expect(t.of('v1/pods').selected.size).toBe(0);
    });

    it('resetInteraction clears selection and focus', () => {
        const t = useTableStore();
        t.select('v1/pods', a);
        t.of('v1/pods').focusId = 7;
        t.resetInteraction('v1/pods');
        expect(t.of('v1/pods').selected.size).toBe(0);
        expect(t.of('v1/pods').focusId).toBe(0);
    });
});
