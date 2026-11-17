import { describe, expect, it } from 'vitest';

import { browserUrl, describeState, forwardKey, isHttpish, portsOf, savedMatches } from '../src/lib/portforward.ts';

const ref = { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'p' };

describe('portsOf', () => {
    it('reads pod container ports', () => {
        const parsed = {
            spec: {
                containers: [
                    {
                        name: 'app',
                        ports: [
                            { containerPort: 8080, name: 'http' },
                            { containerPort: 9, protocol: 'UDP' }
                        ]
                    },
                    { name: 's' }
                ]
            }
        };
        expect(portsOf('v1/pods', parsed)).toEqual([
            { port: 8080, name: 'http', protocol: undefined, containerName: 'app' },
            { port: 9, name: undefined, protocol: 'UDP', containerName: 'app' }
        ]);
    });
    it('reads service ports', () => {
        expect(portsOf('v1/services', { spec: { ports: [{ port: 80, name: 'web', protocol: 'TCP' }] } })).toEqual([
            { port: 80, name: 'web', protocol: 'TCP' }
        ]);
    });
    it('tolerates junk', () => {
        expect(portsOf('v1/pods', null)).toEqual([]);
        expect(portsOf('v1/services', { spec: { ports: 'x' } })).toEqual([]);
        expect(portsOf('apps/v1/deployments', { spec: {} })).toEqual([]);
    });
});

describe('helpers', () => {
    it('isHttpish', () => {
        expect(isHttpish({ port: 8080 })).toBe(true);
        expect(isHttpish({ port: 5432 })).toBe(false);
        expect(isHttpish({ port: 1234, name: 'http-metrics' })).toBe(true);
        expect(isHttpish({ port: 53, protocol: 'UDP' })).toBe(false);
    });
    it('browserUrl and forwardKey', () => {
        expect(browserUrl(4000)).toBe('http://127.0.0.1:4000');
        expect(forwardKey(ref, 80)).toBe('c|v1/pods|ns|p|80');
    });
    it('savedMatches ignores stopped', () => {
        const a = { id: '1', ref, port: 80, localPort: 5000, pod: 'p', state: 'running', usedContext: '' };
        expect(savedMatches({ ref, port: 80 }, [a])).toBe(a);
        expect(savedMatches({ ref, port: 80 }, [{ ...a, state: 'stopped' }])).toBeUndefined();
        expect(savedMatches({ ref, port: 81 }, [a])).toBeUndefined();
    });
    it('describeState', () => {
        expect(describeState('running').tone).toBe('success');
        expect(describeState('reconnecting').tone).toBe('warning');
        expect(describeState('stopped')).toEqual({ tone: 'neutral', text: 'stopped' });
    });
});
