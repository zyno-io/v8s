import { describe, expect, it } from 'vitest';

import {
    cpuMillis,
    cpuShort,
    memMiB,
    memShort,
    outside,
    parseQuantity,
    podRequests,
    vpaNameFromAnnotation,
    vpaRecommendations,
    vpaTarget
} from '../src/lib/vpa.ts';

describe('parseQuantity', () => {
    it('reads decimal and binary suffixes, exponents and decimals', () => {
        expect(parseQuantity('250m')).toBeCloseTo(0.25);
        expect(parseQuantity('1500n')).toBeCloseTo(1.5e-6);
        expect(parseQuantity('2k')).toBe(2000);
        expect(parseQuantity('3M')).toBe(3e6);
        expect(parseQuantity('1G')).toBe(1e9);
        expect(parseQuantity('1T')).toBe(1e12);
        expect(parseQuantity('1Ki')).toBe(1024);
        expect(parseQuantity('1Mi')).toBe(1048576);
        expect(parseQuantity('1.5Gi')).toBe(1.5 * 1024 ** 3);
        expect(parseQuantity('1Ti')).toBe(1024 ** 4);
        expect(parseQuantity('129e6')).toBe(129e6);
        expect(parseQuantity('1E3')).toBe(1000);
        expect(parseQuantity('1E')).toBe(1e18);
        expect(parseQuantity('.5')).toBe(0.5);
        expect(parseQuantity('2')).toBe(2);
    });
    it('is null for garbage', () => {
        for (const v of ['', 'abc', '1Xi', '1 m', undefined, null, {}]) expect(parseQuantity(v)).toBeNull();
    });
});

describe('cpuMillis / memMiB', () => {
    it('rounds cpu up and memory down', () => {
        expect(cpuMillis('25m')).toBe(25);
        expect(cpuMillis('1.5')).toBe(1500);
        expect(cpuMillis('0.1')).toBe(100);
        expect(cpuMillis('1500u')).toBe(2);
        expect(cpuMillis('1n')).toBe(1);
        expect(cpuMillis(undefined)).toBeNull();
        expect(memMiB('512Mi')).toBe(512);
        expect(memMiB('1Gi')).toBe(1024);
        expect(memMiB('262144k')).toBe(250);
        expect(memMiB('1500Ki')).toBe(1);
        expect(memMiB('100k')).toBe(0);
        expect(memMiB('garbage')).toBeNull();
    });
});

describe('short text', () => {
    it('formats compactly', () => {
        expect(cpuShort(250)).toBe('250m');
        expect(cpuShort(1500)).toBe('1.5 cores');
        expect(cpuShort(null)).toBe('—');
        expect(memShort(512)).toBe('512Mi');
        expect(memShort(1536)).toBe('1.5Gi');
        expect(memShort(null)).toBe('—');
    });
});

const VPA = {
    spec: { targetRef: { kind: 'Deployment', name: 'api' } },
    status: {
        recommendation: {
            containerRecommendations: [
                {
                    containerName: 'app',
                    target: { cpu: '25m', memory: '262144k' },
                    lowerBound: { cpu: '10m', memory: '100Mi' },
                    upperBound: { cpu: '1', memory: '1Gi' },
                    uncappedTarget: { cpu: '20m' }
                }
            ]
        }
    }
};

describe('vpaRecommendations', () => {
    it('maps each container, null where a resource is absent', () => {
        expect(vpaRecommendations(VPA)).toEqual([
            {
                container: 'app',
                target: { cpu: 25, mem: 250 },
                lower: { cpu: 10, mem: 100 },
                upper: { cpu: 1000, mem: 1024 },
                uncapped: { cpu: 20, mem: null }
            }
        ]);
    });
    it('is empty without a recommendation', () => {
        expect(vpaRecommendations({ status: {} })).toEqual([]);
        expect(vpaRecommendations(null)).toEqual([]);
    });
});

describe('podRequests', () => {
    it('reads each container’s requests', () => {
        const pod = { spec: { containers: [{ name: 'app', resources: { requests: { cpu: '100m', memory: '128Mi' } } }, { name: 'side' }] } };
        expect(podRequests(pod)).toEqual([
            { container: 'app', cpu: 100, mem: 128 },
            { container: 'side', cpu: null, mem: null }
        ]);
        expect(podRequests(null)).toEqual([]);
    });
});

describe('vpaNameFromAnnotation', () => {
    it('takes the name between the prefix and the first colon', () => {
        expect(vpaNameFromAnnotation('Pod resources updated by api-vpa: container 0: cpu request, memory request')).toEqual({
            name: 'api-vpa',
            updated: 'container 0: cpu request, memory request',
            perContainer: { '0': 'cpu request, memory request' }
        });
    });
    it('splits containers', () => {
        const u = vpaNameFromAnnotation('Pod resources updated by v: container 0: cpu request; container 1: memory request, memory limit');
        expect(u?.perContainer).toEqual({ '0': 'cpu request', '1': 'memory request, memory limit' });
    });
    it('is null for anything else', () => {
        expect(vpaNameFromAnnotation('')).toBeNull();
        expect(vpaNameFromAnnotation(undefined)).toBeNull();
        expect(vpaNameFromAnnotation('something else')).toBeNull();
        expect(vpaNameFromAnnotation('Pod resources updated by : x')).toBeNull();
    });
});

describe('vpaTarget / outside', () => {
    it('names the target', () => {
        expect(vpaTarget(VPA)).toBe('Deployment/api');
        expect(vpaTarget({})).toBe('');
    });
    it('flags a request outside the range', () => {
        expect(outside(5, 10, 100)).toBe(true);
        expect(outside(500, 10, 100)).toBe(true);
        expect(outside(50, 10, 100)).toBe(false);
        expect(outside(null, 10, 100)).toBe(false);
        expect(outside(50, null, null)).toBe(false);
    });
});
