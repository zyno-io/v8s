import { describe, expect, it } from 'vitest';

import { conditionsOf, conditionTone, keyFields, objectStatus, podContainers, revisionOf } from '../src/lib/overview.ts';
import { drawerQuery, gvrFromRoute, gvrPath, parseDrawer } from '../src/lib/route.ts';
import { gvrFromManifest, templateFor, TEMPLATES } from '../src/lib/templates.ts';

describe('objectStatus', () => {
    it('summarizes replicas for workloads', () => {
        expect(objectStatus('apps/v1/deployments', { spec: { replicas: 3 }, status: { readyReplicas: 3 } })).toEqual({
            text: '3/3 ready',
            tone: 'success'
        });
        expect(objectStatus('apps/v1/deployments', { spec: { replicas: 3 }, status: { readyReplicas: 1 } })?.tone).toBe('warning');
        expect(objectStatus('apps/v1/deployments', { spec: { replicas: 3 }, status: {} })?.tone).toBe('danger');
        expect(objectStatus('apps/v1/deployments', { spec: { replicas: 0 } })?.tone).toBe('neutral');
    });
    it('uses phase and conditions elsewhere, and terminating wins', () => {
        expect(objectStatus('v1/pods', { status: { phase: 'Running' } })).toEqual({ text: 'Running', tone: 'success' });
        expect(objectStatus('v1/pods', { status: { phase: 'Pending' } })?.tone).toBe('warning');
        expect(objectStatus('v1/pods', { status: { phase: 'Failed' } })?.tone).toBe('danger');
        expect(objectStatus('x/v1/things', { status: { conditions: [{ type: 'Ready', status: 'False' }] } })).toEqual({
            text: 'Ready false',
            tone: 'danger'
        });
        expect(objectStatus('v1/pods', { metadata: { deletionTimestamp: 'now' }, status: { phase: 'Running' } })?.text).toBe('Terminating');
        expect(objectStatus('batch/v1/cronjobs', { spec: { suspend: true } })).toEqual({ text: 'Suspended', tone: 'neutral' });
        expect(objectStatus('v1/nodes', { spec: { unschedulable: true }, status: { conditions: [{ type: 'Ready', status: 'True' }] } })?.text).toBe(
            'SchedulingDisabled'
        );
        expect(objectStatus('v1/pods', null)).toBeNull();
    });
});

describe('conditions', () => {
    it('reads status.conditions and tones them', () => {
        const c = conditionsOf({
            status: {
                conditions: [
                    { type: 'Available', status: 'True', reason: 'ok', lastTransitionTime: 't' },
                    { type: 'MemoryPressure', status: 'True' }
                ]
            }
        });
        expect(c.map(x => x.type)).toEqual(['Available', 'MemoryPressure']);
        expect(conditionTone(c[0])).toBe('success');
        expect(conditionTone(c[1])).toBe('danger');
        expect(conditionTone({ type: 'Ready', status: 'Unknown' })).toBe('warning');
        expect(conditionsOf(null)).toEqual([]);
    });
});

describe('keyFields and revision', () => {
    it('extracts replicas and the revision annotation', () => {
        const dep = {
            metadata: { annotations: { 'deployment.kubernetes.io/revision': '4' } },
            spec: { replicas: 2, strategy: { type: 'RollingUpdate' } },
            status: { readyReplicas: 1 }
        };
        expect(keyFields('apps/v1/deployments', dep).find(f => f.label === 'Replicas')?.value).toBe('1 ready of 2');
        expect(revisionOf(dep)).toBe(4);
        expect(revisionOf({})).toBe(0);
    });
});

describe('podContainers', () => {
    it('lists init, sidecar, regular and ephemeral containers with image and state', () => {
        const list = podContainers({
            spec: {
                initContainers: [
                    { name: 'migrate', image: 'app:1-migrate' },
                    { name: 'proxy', image: 'envoy:1.30', restartPolicy: 'Always' }
                ],
                containers: [
                    { name: 'app', image: 'ghcr.io/x/app:1.2.3' },
                    { name: 'worker', image: 'ghcr.io/x/worker:1.2.3' },
                    { name: 'new', image: 'busybox' }
                ],
                ephemeralContainers: [{ name: 'debugger', image: 'nicolaka/netshoot' }]
            },
            status: {
                initContainerStatuses: [
                    { name: 'migrate', ready: false, restartCount: 0, state: { terminated: { exitCode: 0, reason: 'Completed' } } },
                    { name: 'proxy', ready: true, restartCount: 1, state: { running: {} } }
                ],
                containerStatuses: [
                    { name: 'app', ready: false, restartCount: 0, state: { running: {} } },
                    { name: 'worker', ready: false, restartCount: 7, state: { waiting: { reason: 'CrashLoopBackOff' } } }
                ],
                ephemeralContainerStatuses: [{ name: 'debugger', state: { terminated: { exitCode: 137, reason: 'Error' } } }]
            }
        });
        expect(list.map(c => [c.name, c.kind, c.image, c.state, c.tone, c.restarts])).toEqual([
            ['migrate', 'init', 'app:1-migrate', 'Completed', 'info', 0],
            ['proxy', 'sidecar', 'envoy:1.30', 'Running', 'success', 1],
            ['app', '', 'ghcr.io/x/app:1.2.3', 'Running', 'warning', 0],
            ['worker', '', 'ghcr.io/x/worker:1.2.3', 'CrashLoopBackOff', 'danger', 7],
            ['new', '', 'busybox', '', 'neutral', 0],
            ['debugger', 'ephemeral', 'nicolaka/netshoot', 'Error (137)', 'danger', 0]
        ]);
        expect(podContainers({ status: { containerStatuses: [{ name: 'x', state: { waiting: { reason: 'ContainerCreating' } } }] } })).toEqual([]);
        expect(
            podContainers({
                spec: { containers: [{ name: 'a', image: 'a' }] },
                status: { containerStatuses: [{ name: 'a', state: { waiting: { reason: 'ContainerCreating' } } }] }
            })[0].tone
        ).toBe('warning');
        expect(podContainers(null)).toEqual([]);
    });
});

describe('route helpers', () => {
    it('maps gvrs to paths and back', () => {
        expect(gvrPath('v1/pods')).toBe('/pods');
        expect(gvrPath('apps/v1/deployments')).toBe('/r/apps/v1/deployments');
        expect(gvrFromRoute('/pods', {})).toBe('v1/pods');
        expect(gvrFromRoute('/r/apps/v1/deployments', { gvr: 'apps/v1/deployments' })).toBe('apps/v1/deployments');
        expect(gvrFromRoute('/problems', {})).toBeNull();
    });
    it('round-trips the drawer through the query', () => {
        const q = drawerQuery({ cluster: 'arn:aws:eks:eu/prod', namespace: 'pay', name: 'api' }, 'yaml');
        expect(q).toEqual({ c: 'arn:aws:eks:eu/prod', ns: 'pay', n: 'api', tab: 'yaml' });
        expect(parseDrawer('apps/v1/deployments', q)).toEqual({
            ref: { cluster: 'arn:aws:eks:eu/prod', gvr: 'apps/v1/deployments', namespace: 'pay', name: 'api' },
            tab: 'yaml'
        });
        expect(parseDrawer('v1/nodes', { c: 'a', n: 'n1' })).toEqual({
            ref: { cluster: 'a', gvr: 'v1/nodes', namespace: undefined, name: 'n1' },
            tab: 'overview'
        });
        expect(parseDrawer('v1/pods', {})).toBeNull();
        expect(parseDrawer(null, { c: 'a', n: 'b' })).toBeNull();
    });
});

describe('templates', () => {
    it('covers the documented kinds and fills the namespace', () => {
        expect(TEMPLATES.map(t => t.kind)).toEqual(['Namespace', 'ConfigMap', 'Secret', 'Deployment', 'Service', 'Ingress', 'CronJob', 'Job']);
        expect(templateFor('v1/configmaps')!.yaml('payments')).toContain('namespace: payments');
        expect(templateFor('v1/namespaces')!.yaml('x')).not.toContain('namespace:');
    });
    it('resolves a gvr from a manifest', () => {
        const kinds = [
            { gvr: 'apps/v1/deployments', group: 'apps', version: 'v1', kind: 'Deployment' },
            { gvr: 'v1/configmaps', group: '', version: 'v1', kind: 'ConfigMap' }
        ] as never[];
        expect(gvrFromManifest({ apiVersion: 'apps/v1', kind: 'Deployment' }, kinds)).toBe('apps/v1/deployments');
        expect(gvrFromManifest({ apiVersion: 'v1', kind: 'ConfigMap' }, kinds)).toBe('v1/configmaps');
        expect(gvrFromManifest({ apiVersion: 'apps/v2', kind: 'Deployment' }, kinds)).toBe('apps/v1/deployments');
        expect(gvrFromManifest({ apiVersion: 'x/v1', kind: 'Nope' }, kinds)).toBeNull();
        expect(gvrFromManifest(null, kinds)).toBeNull();
    });
});
