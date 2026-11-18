import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const drawer = vi.hoisted(() => ({ open: false, close: vi.fn() }));
vi.mock('../src/router.ts', () => ({ default: {} }));
vi.mock('../src/lib/drawer.ts', () => ({
    isDrawerOpen: () => drawer.open,
    closeDrawer: () => {
        drawer.open = false;
        drawer.close();
    }
}));

import { escapeChain, installGlobalKeys, setListNav, type ListNav } from '../src/lib/keys.ts';
import { useTableStore } from '../src/stores/table.ts';

// Node has no DOM: the smallest stand-ins the key handler touches.
class FakeEl {
    isContentEditable = false;
    constructor(
        public tagName: string,
        public id = '',
        public type = 'text'
    ) {}
    blur = vi.fn();
    closest() {
        return null;
    }
}
class FakeInput extends FakeEl {
    constructor(id = '') {
        super('INPUT', id, 'search');
    }
}

let keydown: (e: unknown) => void;
const body = new FakeEl('BODY');
const filter = new FakeInput('pods-filter');
const list = new FakeEl('DIV');

function key(key: string, target: unknown, extra: Record<string, unknown> = {}) {
    const e = {
        key,
        target,
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
        isComposing: false,
        defaultPrevented: false,
        preventDefault: vi.fn(),
        ...extra
    };
    keydown(e);
    return e;
}

function nav(over: Partial<ListNav> = {}): ListNav & { focusList: ReturnType<typeof vi.fn> } {
    return { focusList: vi.fn(() => true), owns: el => (el as unknown) === list, ...over } as ListNav & { focusList: ReturnType<typeof vi.fn> };
}

let uninstall: () => void;
beforeEach(() => {
    setActivePinia(createPinia());
    drawer.open = false;
    drawer.close.mockClear();
    vi.stubGlobal('HTMLElement', FakeEl);
    vi.stubGlobal('HTMLInputElement', FakeInput);
    vi.stubGlobal('document', { body });
    vi.stubGlobal('window', {
        addEventListener: (_: string, h: (e: unknown) => void) => (keydown = h),
        removeEventListener: () => {}
    });
    vi.stubGlobal('matchMedia', () => ({ matches: false }));
    uninstall = installGlobalKeys(() => {});
});
afterEach(() => {
    uninstall();
    setListNav(null);
    vi.unstubAllGlobals();
});

describe('Enter in the filter', () => {
    it('hands focus to the list and consumes the key', () => {
        const n = nav();
        setListNav(n);
        const e = key('Enter', filter);
        expect(n.focusList).toHaveBeenCalledWith(false);
        expect(e.preventDefault).toHaveBeenCalled();
    });

    it('does nothing with no results', () => {
        const n = nav({ focusList: vi.fn(() => false) });
        setListNav(n);
        const e = key('Enter', filter);
        expect(e.preventDefault).not.toHaveBeenCalled();
    });

    it('ignores IME composition and other inputs', () => {
        const n = nav();
        setListNav(n);
        key('Enter', filter, { isComposing: true });
        key('Enter', new FakeInput('other'));
        expect(n.focusList).not.toHaveBeenCalled();
    });
});

describe('Esc', () => {
    it('from the list clears a page filter, refocuses the list, then continues the chain', async () => {
        let text = 'abc';
        const n = nav({ filterText: () => text, clearFilter: () => (text = '') });
        setListNav(n);
        drawer.open = true;
        key('Escape', list);
        expect(text).toBe('');
        expect(drawer.close).not.toHaveBeenCalled();
        await Promise.resolve();
        await Promise.resolve();
        expect(n.focusList).toHaveBeenCalledWith(true);
        n.focusList.mockClear();
        key('Escape', list);
        expect(drawer.close).toHaveBeenCalledTimes(1);
        await Promise.resolve();
        expect(n.focusList).not.toHaveBeenCalled();
    });

    it('with nothing focused also refocuses the list after clearing the table filter', async () => {
        const table = useTableStore();
        table.currentGvr = 'v1/pods';
        table.of('v1/pods').text = 'pay';
        const n = nav();
        setListNav(n);
        key('Escape', body);
        expect(table.of('v1/pods').text).toBe('');
        await Promise.resolve();
        await Promise.resolve();
        expect(n.focusList).toHaveBeenCalledWith(true);
    });

    it('inside the filter clears it without moving focus; empty filter blurs', async () => {
        let text = 'abc';
        const n = nav({ filterText: () => text, clearFilter: () => (text = '') });
        setListNav(n);
        key('Escape', filter);
        expect(text).toBe('');
        await Promise.resolve();
        await Promise.resolve();
        expect(n.focusList).not.toHaveBeenCalled();
        key('Escape', filter);
        expect(filter.blur).toHaveBeenCalled();
    });

    it('escapeChain order: filter, selection, drawer', () => {
        const table = useTableStore();
        table.currentGvr = 'v1/pods';
        const st = table.of('v1/pods');
        st.text = 'x';
        st.selected = new Map([['k', { cluster: 'c', name: 'n' } as never]]);
        drawer.open = true;
        expect(escapeChain()).toBe(true);
        expect(st.text).toBe('');
        expect(st.selected.size).toBe(1);
        expect(escapeChain()).toBe(true);
        expect(st.selected.size).toBe(0);
        expect(escapeChain()).toBe(true);
        expect(drawer.close).toHaveBeenCalled();
        expect(escapeChain()).toBe(false);
    });
});
