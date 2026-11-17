import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it } from 'vitest';

import type { ConfirmDialog } from '../src/stores/ui.ts';

import { ask } from '../src/lib/ask.ts';
import { useUiStore } from '../src/stores/ui.ts';

beforeEach(() => setActivePinia(createPinia()));

describe('ask queue', () => {
    it('queues prompts instead of replacing the open one', async () => {
        const ui = useUiStore();
        const cur = () => ui.dialog as ConfirmDialog | null;
        const a = ask({ title: 'A', body: '', confirmLabel: 'Ok' });
        const b = ask({ title: 'B', body: '', confirmLabel: 'Ok' });
        expect(cur()?.title).toBe('A');
        cur()?.onCancel?.();
        ui.dialog = null;
        await expect(a).resolves.toBe(false);
        expect(cur()?.title).toBe('B');
        void cur()?.run();
        ui.dialog = null;
        await expect(b).resolves.toBe(true);
        expect(ui.dialog).toBeNull();
    });
});
