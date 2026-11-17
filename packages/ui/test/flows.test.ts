import { describe, expect, it } from 'vitest';

import {
    emptyFilter,
    filterKey,
    flowScope,
    flowTime,
    noRelay,
    portProto,
    rateText,
    relevantInputs,
    reopenDecision,
    toFlowFilter,
    verdictFromQuery,
    verdictTone
} from '../src/lib/flows.ts';

describe('flowScope', () => {
    it('pods use the pod scope', () => {
        expect(flowScope({ gvr: 'v1/pods', namespace: 'shop', name: 'web-1' })).toEqual({ namespace: 'shop', pod: 'web-1' });
    });
    it('workloads and services use the workload scope', () => {
        for (const gvr of [
            'apps/v1/deployments',
            'apps/v1/statefulsets',
            'apps/v1/daemonsets',
            'apps/v1/replicasets',
            'batch/v1/jobs',
            'v1/services'
        ]) {
            expect(flowScope({ gvr, namespace: 'shop', name: 'web' })).toEqual({ namespace: 'shop', workload: 'web' });
        }
    });
    it('namespaces scope to themselves', () => {
        expect(flowScope({ gvr: 'v1/namespaces', name: 'shop' })).toEqual({ namespace: 'shop' });
    });
    it('refuses unscoped or unsupported objects', () => {
        expect(flowScope({ gvr: 'v1/pods', name: 'x' })).toBeNull();
        expect(flowScope({ gvr: 'v1/configmaps', namespace: 'a', name: 'x' })).toBeNull();
    });
});

describe('filter mapping', () => {
    it('omits empty fields', () => {
        expect(toFlowFilter(emptyFilter())).toEqual({});
    });
    it('maps selects and trims text', () => {
        const f = { ...emptyFilter(), verdict: 'DROPPED', direction: 'EGRESS', dropReason: ' policy_denied ' };
        expect(toFlowFilter(f)).toEqual({ verdict: 'DROPPED', direction: 'EGRESS', dropReason: 'POLICY_DENIED' });
    });
    it('only sends inputs relevant to the protocol and verdict', () => {
        const f = { ...emptyFilter(), protocol: 'http', httpStatus: '5+', dnsQuery: 'x.com', verdict: 'FORWARDED', dropReason: 'X' };
        expect(toFlowFilter(f)).toEqual({ protocol: 'http', httpStatus: '5+', verdict: 'FORWARDED' });
        expect(relevantInputs({ ...emptyFilter(), protocol: 'dns' }).dnsQuery).toBe(true);
        expect(toFlowFilter({ ...emptyFilter(), protocol: 'dns', dnsQuery: 'a.b' })).toEqual({ protocol: 'dns', dnsQuery: 'a.b' });
    });
    it('reads the verdict query param', () => {
        expect(verdictFromQuery('dropped')).toBe('DROPPED');
        expect(verdictFromQuery(['ERROR'])).toBe('ERROR');
        expect(verdictFromQuery('nonsense')).toBe('');
        expect(verdictFromQuery(undefined)).toBe('');
    });
});

describe('reopenDecision', () => {
    const base = emptyFilter();
    it('does nothing when the relay filter is unchanged', () => {
        expect(reopenDecision(base, { ...base })).toBe('none');
        // an irrelevant input changing does not reopen
        expect(reopenDecision(base, { ...base, httpStatus: '5+' })).toBe('none');
        expect(filterKey(base)).toBe('{}');
    });
    it('reopens at once for selects and debounces typing', () => {
        expect(reopenDecision(base, { ...base, verdict: 'DROPPED' })).toBe('now');
        expect(reopenDecision(base, { ...base, dropReason: 'POL' })).toBe('debounced');
        expect(reopenDecision(base, { ...base, verdict: 'DROPPED', dropReason: 'P' })).toBe('debounced');
    });
});

describe('display helpers', () => {
    it('rate text', () => {
        expect(rateText(null)).toBe('');
        expect(rateText({ shown: 40, total: 40, sampled: false })).toBe('40 flows/s');
        expect(rateText({ shown: 200, total: 950, sampled: true })).toBe('showing 200/s of ~950/s, narrow the filter');
    });
    it('verdict tones', () => {
        expect(verdictTone('FORWARDED')).toBe('success');
        expect(verdictTone('DROPPED')).toBe('danger');
        expect(verdictTone('ERROR')).toBe('danger');
        expect(verdictTone('AUDIT')).toBe('info');
    });
    it('formats time and ports', () => {
        expect(flowTime(0)).toBe('');
        expect(flowTime(new Date(2024, 0, 1, 3, 4, 5, 6).getTime())).toBe('03:04:05.006');
        expect(portProto({ dst: { port: 8080 }, protocol: 'TCP' })).toBe('8080/TCP');
        expect(portProto({ dst: {} })).toBe('');
    });
    it('detects a missing relay', () => {
        expect(noRelay({ code: 'unavailable', message: 'no relay', hint: 'h' })).toEqual({ message: 'no relay', hint: 'h' });
        expect(noRelay({ code: 'internal', message: 'x' })).toBeNull();
        expect(noRelay(new Error('x'))).toBeNull();
    });
});
