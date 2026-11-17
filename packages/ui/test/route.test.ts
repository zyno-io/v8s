import { describe, expect, it } from 'vitest';

import { parseDrawer, tabsFor } from '../src/lib/route.ts';

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
