import { describe, expect, it } from 'vitest';

import { carriedTab, parseDrawer, tabsFor } from '../src/lib/route.ts';

describe('tabsFor events', () => {
    it('offers the Events tab for ordinary kinds', () => {
        expect(tabsFor('v1/pods')).toContain('events');
        expect(tabsFor('apps/v1/deployments')).toContain('events');
        expect(tabsFor('v1/nodes')).toContain('events');
    });

    it('does not offer it for Event kinds, which keep their other tabs', () => {
        for (const gvr of ['v1/events', 'events.k8s.io/v1/events']) {
            const tabs = tabsFor(gvr);
            expect(tabs).not.toContain('events');
            expect(tabs).toEqual(expect.arrayContaining(['overview', 'yaml', 'related']));
        }
    });

    it('opens the Overview for an old ?tab=events link on an Event', () => {
        expect(parseDrawer('v1/events', { c: 'prod', ns: 'team', n: 'e1', tab: 'events' })?.tab).toBe('overview');
        expect(parseDrawer('v1/pods', { c: 'prod', ns: 'team', n: 'p', tab: 'events' })?.tab).toBe('events');
    });
});

describe('carriedTab', () => {
    const open = (tab?: string) => ({ c: 'prod', ns: 'team', n: 'api-1', ...(tab ? { tab } : {}) });
    it("keeps the open drawer's tab when the new object has it", () => {
        expect(carriedTab(open('logs'), 'v1/pods')).toBe('logs');
        expect(carriedTab(open('yaml'), 'apps/v1/replicasets')).toBe('yaml');
    });
    it('falls back (undefined) when the new type lacks the tab, the drawer is closed, or it is on Overview', () => {
        expect(carriedTab(open('shell'), 'apps/v1/replicasets')).toBeUndefined();
        expect(carriedTab({ tab: 'logs' }, 'v1/pods')).toBeUndefined();
        expect(carriedTab(open(), 'v1/pods')).toBeUndefined();
        expect(carriedTab(open('bogus'), 'v1/pods')).toBeUndefined();
    });
});
