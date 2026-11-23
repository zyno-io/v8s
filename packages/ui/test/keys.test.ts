import { ProblemsGVR } from '@v8s/client';
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

import { kinds } from '../src/lib/agent.ts';
import {
    escapeChain,
    installGlobalKeys,
    restoreListFocus,
    setDrawerNav,
    setListNav,
    setPageKeyHandler,
    type DrawerNav,
    type ListNav
} from '../src/lib/keys.ts';
import { recordRecentNamespaces, setRecentKey } from '../src/lib/recent-namespaces.ts';
import { useTableStore } from '../src/stores/table.ts';

// Node has no DOM: the smallest stand-ins the key handler touches.
class FakeEl {
    isContentEditable = false;
    constructor(
        public tagName: string,
        public id = '',
        public type = 'text'
    ) {}
    attrs = new Set<string>();
    hasAttribute(name: string) {
        return this.attrs.has(name);
    }
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
    setDrawerNav(null);
    setPageKeyHandler(null);
    vi.unstubAllGlobals();
});

describe('inside an open native dialog', () => {
    it('row keys and Ctrl+D do not reach the page', () => {
        const h = vi.fn(() => true);
        setPageKeyHandler(h);
        const radio = new FakeEl('INPUT', 'r', 'radio');
        radio.closest = () => ({}) as never;
        key('d', radio, { ctrlKey: true });
        key('o', radio);
        expect(h).not.toHaveBeenCalled();
    });
});

describe('Ctrl+D', () => {
    it('reaches the page handler from the list, and is consumed when handled', () => {
        const h = vi.fn(() => true);
        setPageKeyHandler(h);
        const e = key('d', list, { ctrlKey: true });
        expect(h).toHaveBeenCalledTimes(1);
        expect(e.preventDefault).toHaveBeenCalled();
    });

    it('is not consumed when the page declines, and ignores typing, Cmd+D and Shift', () => {
        const h = vi.fn(() => false);
        setPageKeyHandler(h);
        expect(key('d', list, { ctrlKey: true }).preventDefault).not.toHaveBeenCalled();
        h.mockClear();
        key('d', filter, { ctrlKey: true });
        key('d', list, { metaKey: true });
        key('D', list, { ctrlKey: true, shiftKey: true });
        expect(h).not.toHaveBeenCalled();
    });
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

describe('select triggers', () => {
    it('keep page shortcuts away like a native select did', () => {
        const handler = vi.fn(() => true);
        setPageKeyHandler(handler);
        const trigger = new FakeEl('BUTTON');
        trigger.attrs.add('data-select');
        key('j', trigger);
        expect(handler).not.toHaveBeenCalled();
        key('j', new FakeEl('BUTTON'));
        expect(handler).toHaveBeenCalledTimes(1);
        setPageKeyHandler(null);
    });
});

describe('Esc', () => {
    it('with a drawer open, closes the drawer first and keeps the search; then clears it from the list', async () => {
        let text = 'abc';
        const n = nav({ filterText: () => text, clearFilter: () => (text = '') });
        setListNav(n);
        drawer.open = true;
        key('Escape', list);
        expect(drawer.close).toHaveBeenCalledTimes(1);
        expect(text).toBe('abc');
        key('Escape', list);
        expect(text).toBe('');
        await Promise.resolve();
        await Promise.resolve();
        expect(n.focusList).toHaveBeenCalledWith(true);
    });

    it('with a drawer open, Esc in the search field clears the search and leaves the drawer', () => {
        let text = 'abc';
        setListNav(nav({ filterText: () => text, clearFilter: () => (text = '') }));
        drawer.open = true;
        key('Escape', filter);
        expect(text).toBe('');
        expect(drawer.close).not.toHaveBeenCalled();
    });

    it('with a drawer open, Esc on the page body closes it even with a table filter set', () => {
        const table = useTableStore();
        table.currentGvr = 'v1/pods';
        table.of('v1/pods').text = 'pay';
        drawer.open = true;
        key('Escape', body);
        expect(drawer.close).toHaveBeenCalledTimes(1);
        expect(table.of('v1/pods').text).toBe('pay');
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

    it('escapeChain order: drawer, filter, selection; from the search field: filter, selection, drawer', () => {
        const filterTarget = filter as unknown as EventTarget;
        const table = useTableStore();
        table.currentGvr = 'v1/pods';
        const st = table.of('v1/pods');
        st.text = 'x';
        st.selected = new Map([['k', { cluster: 'c', name: 'n' } as never]]);
        drawer.open = true;
        expect(escapeChain()).toBe(true);
        expect(drawer.close).toHaveBeenCalledTimes(1);
        expect(st.text).toBe('x');
        expect(escapeChain()).toBe(true);
        expect(st.text).toBe('');
        expect(escapeChain()).toBe(true);
        expect(st.selected.size).toBe(0);
        expect(escapeChain()).toBe(false);

        st.text = 'y';
        st.selected = new Map([['k', { cluster: 'c', name: 'n' } as never]]);
        drawer.open = true;
        expect(escapeChain(filterTarget)).toBe(true);
        expect(st.text).toBe('');
        expect(escapeChain(filterTarget)).toBe(true);
        expect(st.selected.size).toBe(0);
        expect(escapeChain(filterTarget)).toBe(true);
        expect(drawer.close).toHaveBeenCalledTimes(2);
    });
});

describe('namespace digits', () => {
    beforeEach(() => {
        const mem = new Map<string, string>();
        vi.stubGlobal('localStorage', { getItem: (k: string) => mem.get(k) ?? null, setItem: (k: string, v: string) => mem.set(k, v) });
        setRecentKey('c1');
        recordRecentNamespaces(['a']);
        recordRecentNamespaces(['b', 'c']);
        useTableStore().currentGvr = 'v1/pods';
    });
    afterEach(() => {
        setRecentKey('');
        kinds.value = [];
    });

    it('1–9 apply the recent sets without reordering them; 0 clears the filter', () => {
        const st = useTableStore().of('v1/pods');
        for (const [k, want] of [
            ['1', ['b', 'c']],
            ['2', ['a']],
            ['1', ['b', 'c']],
            ['2', ['a']],
            ['0', []],
            ['2', ['a']]
        ] as const) {
            expect(key(k, body).preventDefault).toHaveBeenCalled();
            expect(st.namespaces).toEqual(want);
        }
        key('9', body); // nothing there: no change
        expect(st.namespaces).toEqual(['a']);
        const same = st.namespaces;
        key('2', body); // already applied: the same array, so the selection and view are left alone
        expect(st.namespaces).toBe(same);
        expect(key('1', body, { repeat: true }).preventDefault).toHaveBeenCalled(); // auto-repeat is swallowed
        expect(st.namespaces).toBe(same);
    });

    it('does nothing on Problems, which shows no namespace menu', () => {
        const table = useTableStore();
        table.currentGvr = ProblemsGVR;
        expect(key('1', body).preventDefault).not.toHaveBeenCalled();
        expect(table.of(ProblemsGVR).namespaces).toEqual([]);
    });

    it('leaves cluster-scoped kinds and typing alone', () => {
        const table = useTableStore();
        key('1', filter);
        expect(table.of('v1/pods').namespaces).toEqual([]);
        kinds.value = [{ gvr: 'v1/nodes', namespaced: false } as never];
        table.currentGvr = 'v1/nodes';
        expect(key('1', body).preventDefault).not.toHaveBeenCalled();
        expect(table.of('v1/nodes').namespaces).toEqual([]);
    });
});

describe('drawer scope', () => {
    let dn: DrawerNav & { scrollKey: ReturnType<typeof vi.fn> };
    const openNav = (scrollKey = vi.fn(() => false)) => {
        dn = { open: () => true, scrollKey, focus: vi.fn() } as typeof dn;
        setDrawerNav(dn);
    };

    it('routes list keys from the drawer and the body to the handler as drawer scope, unconsumed', () => {
        openNav();
        setListNav(nav());
        const h = vi.fn(() => false);
        setPageKeyHandler(h);
        const inDrawer = new FakeEl('DIV');
        for (const k of ['j', 'ArrowDown', 'x', 'Enter']) {
            for (const t of [inDrawer, body]) {
                const e = key(k, t);
                expect(h).toHaveBeenLastCalledWith(e, 'drawer');
                expect(e.preventDefault).not.toHaveBeenCalled();
            }
        }
    });

    it('routes the same keys from the list as list scope', () => {
        openNav();
        setListNav(nav());
        const h = vi.fn(() => true);
        setPageKeyHandler(h);
        const e = key('ArrowDown', list);
        expect(h).toHaveBeenCalledWith(e, 'list');
        expect(e.preventDefault).toHaveBeenCalled();
    });

    it('a scroll key the drawer takes is consumed and never reaches the page', () => {
        openNav(vi.fn(() => true));
        setListNav(nav());
        const h = vi.fn(() => false);
        setPageKeyHandler(h);
        const e = key('ArrowDown', body);
        expect(e.preventDefault).toHaveBeenCalled();
        expect(h).not.toHaveBeenCalled();
    });

    it('Ctrl+D from the drawer is drawer scope', () => {
        openNav();
        setListNav(nav());
        const h = vi.fn(() => true);
        setPageKeyHandler(h);
        const e = key('d', body, { ctrlKey: true });
        expect(h).toHaveBeenCalledWith(e, 'drawer');
    });

    it('digits do nothing from the drawer; the namespaces stay', () => {
        openNav();
        setListNav(nav());
        const st = useTableStore();
        st.currentGvr = 'v1/pods';
        st.of('v1/pods').namespaces = ['keep'];
        key('0', body);
        expect(st.of('v1/pods').namespaces).toEqual(['keep']);
    });

    it('a closed drawer behaves as the list', () => {
        setDrawerNav({ open: () => false, scrollKey: vi.fn(() => false), focus: vi.fn() });
        setListNav(nav());
        const h = vi.fn(() => true);
        setPageKeyHandler(h);
        const e = key('j', body);
        expect(h).toHaveBeenCalledWith(e, 'list');
    });

    it('Esc in a read-only editor closes the drawer; in an editable one it only blurs', () => {
        setListNav(nav());
        drawer.open = true;
        const ro = new FakeEl('DIV');
        ro.closest = (sel?: string) => (sel === '.cm-editor' ? ({ querySelector: () => null } as never) : null);
        key('Escape', ro);
        expect(drawer.close).toHaveBeenCalledTimes(1);
        drawer.open = true;
        const rw = new FakeEl('DIV');
        rw.closest = (sel?: string) => (sel === '.cm-editor' ? ({ querySelector: () => ({}) } as never) : null);
        key('Escape', rw);
        expect(rw.blur).toHaveBeenCalled();
        expect(drawer.close).toHaveBeenCalledTimes(1);
    });

    it('restoreListFocus forwards the ref to the list', () => {
        const restoreFocus = vi.fn();
        setListNav(nav({ restoreFocus }));
        const ref = { cluster: 'c', gvr: 'v1/pods', name: 'p' };
        restoreListFocus(ref);
        expect(restoreFocus).toHaveBeenCalledWith(ref);
    });
});
