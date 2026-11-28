import { isVpaGvr, vpaTarget } from './vpa.ts';

export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'release' | 'neutral';

export interface StatusTag {
    text: string;
    tone: Tone;
}

export interface Condition {
    type: string;
    status: string;
    reason: string;
    message: string;
    lastTransition: string;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v: unknown): string => (v === undefined || v === null ? '' : String(v));
const get = (o: unknown, path: string): unknown => path.split('.').reduce<unknown>((a, k) => (isObj(a) ? a[k] : undefined), o);

export function conditionsOf(obj: Obj | null): Condition[] {
    const raw = get(obj, 'status.conditions');
    if (!Array.isArray(raw)) return [];
    return raw.filter(isObj).map(c => ({
        type: str(c.type),
        status: str(c.status),
        reason: str(c.reason),
        message: str(c.message),
        lastTransition: str(c.lastTransitionTime ?? c.lastUpdateTime ?? c.lastProbeTime)
    }));
}

/** Tone for a condition: Ready/Available-like types are good when True; Failure-like types are bad when True. */
export function conditionTone(c: Pick<Condition, 'type' | 'status'>): Tone {
    const negative = /pressure|unavailable|failure|failed|degraded|stalled|unschedulable|networkunavailable/i.test(c.type);
    if (c.status === 'Unknown') return 'warning';
    if (negative) return c.status === 'True' ? 'danger' : 'success';
    return c.status === 'True' ? 'success' : 'danger';
}

/** A single status tag for an object, from its own fields (conditions, phase, replicas). Always a source word. */
export function objectStatus(gvr: string, obj: Obj | null): StatusTag | null {
    if (!obj) return null;
    if (get(obj, 'metadata.deletionTimestamp')) return { text: 'Terminating', tone: 'neutral' };
    if (gvr === 'apps/v1/deployments' || gvr === 'apps/v1/statefulsets' || gvr === 'apps/v1/replicasets') {
        const want = Number(get(obj, 'spec.replicas') ?? 1);
        const ready = Number(get(obj, 'status.readyReplicas') ?? 0);
        const text = `${ready}/${want} ready`;
        return { text, tone: want === 0 ? 'neutral' : ready >= want ? 'success' : ready === 0 ? 'danger' : 'warning' };
    }
    if (gvr === 'batch/v1/cronjobs') return get(obj, 'spec.suspend') ? { text: 'Suspended', tone: 'neutral' } : { text: 'Active', tone: 'success' };
    if (gvr === 'v1/nodes') {
        const ready = conditionsOf(obj).find(c => c.type === 'Ready');
        const cordoned = !!get(obj, 'spec.unschedulable');
        if (ready?.status !== 'True') return { text: 'NotReady', tone: 'danger' };
        return cordoned ? { text: 'SchedulingDisabled', tone: 'warning' } : { text: 'Ready', tone: 'success' };
    }
    const phase = str(get(obj, 'status.phase'));
    if (phase) {
        const tone: Tone = /^(running|active|bound|available|succeeded|complete)/i.test(phase)
            ? /succeeded|complete/i.test(phase)
                ? 'info'
                : 'success'
            : /fail|error|lost/i.test(phase)
              ? 'danger'
              : /pending|terminating/i.test(phase)
                ? 'warning'
                : 'neutral';
        return { text: phase, tone };
    }
    const conds = conditionsOf(obj);
    const ready = conds.find(c => ['Ready', 'Available', 'Healthy', 'Synced'].includes(c.type)) ?? conds[0];
    if (ready)
        return {
            text: ready.status === 'True' ? ready.type : ready.type + ' ' + (ready.status === 'False' ? 'false' : 'unknown'),
            tone: conditionTone(ready)
        };
    return null;
}

export interface Field {
    label: string;
    value: string;
    mono?: boolean;
}

/** Key fields per kind, with a generic fallback of the spec's top-level scalars. */
export function keyFields(gvr: string, obj: Obj | null): Field[] {
    if (!obj) return [];
    const f: Field[] = [];
    const add = (label: string, value: unknown, mono = false) => {
        const v = str(value);
        if (v) f.push({ label, value: v, mono });
    };
    switch (isVpaGvr(gvr) ? 'vpa' : gvr) {
        case 'vpa':
            add('Target', vpaTarget(obj));
            add('Update mode', get(obj, 'spec.updatePolicy.updateMode') ?? 'Auto');
            break;
        case 'apps/v1/deployments':
        case 'apps/v1/statefulsets':
        case 'apps/v1/replicasets':
            add('Replicas', `${str(get(obj, 'status.readyReplicas') ?? 0)} ready of ${str(get(obj, 'spec.replicas') ?? 1)}`);
            add('Strategy', get(obj, 'spec.strategy.type') ?? get(obj, 'spec.updateStrategy.type'));
            add('Revision', get(obj, 'metadata.annotations')?.['deployment.kubernetes.io/revision' as never], true);
            break;
        case 'v1/pods':
            add('Node', get(obj, 'spec.nodeName'), true);
            add('Pod IP', get(obj, 'status.podIP'), true);
            add('Phase', get(obj, 'status.phase'));
            break;
        case 'v1/services':
            add('Type', get(obj, 'spec.type'));
            add('Cluster IP', get(obj, 'spec.clusterIP'), true);
            break;
        case 'batch/v1/cronjobs':
            add('Schedule', get(obj, 'spec.schedule'), true);
            add('Suspended', get(obj, 'spec.suspend') ? 'yes' : 'no');
            break;
        case 'v1/nodes':
            add('Kubelet', get(obj, 'status.nodeInfo.kubeletVersion'), true);
            add('OS image', get(obj, 'status.nodeInfo.osImage'));
            break;
        default: {
            const spec = get(obj, 'spec');
            if (isObj(spec)) {
                for (const [k, v] of Object.entries(spec)) {
                    if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean')
                        add(
                            k.replace(/([A-Z])/g, ' $1').replace(/^./, s => s.toUpperCase()),
                            v
                        );
                    if (f.length >= 6) break;
                }
            }
        }
    }
    return f;
}

/** Deployment revision (from the annotation), for the rollback dialog's default. */
export function revisionOf(obj: Obj | null): number {
    const a = get(obj, 'metadata.annotations');
    const v = isObj(a) ? Number(a['deployment.kubernetes.io/revision']) : NaN;
    return Number.isFinite(v) ? v : 0;
}

export function stringMap(v: unknown): [string, string][] {
    return isObj(v)
        ? Object.entries(v)
              .map(([k, x]) => [k, str(x)] as [string, string])
              .sort((a, b) => a[0].localeCompare(b[0]))
        : [];
}

export const GITOPS_LABEL: Record<string, string> = { Kustomization: 'Flux', HelmRelease: 'Flux', Helm: 'Helm' };

export interface PodContainer {
    name: string;
    /** '' for a regular container; init containers with `restartPolicy: Always` are sidecars. */
    kind: '' | 'init' | 'sidecar' | 'ephemeral';
    image: string;
    /** Running, Completed, CrashLoopBackOff, …; '' before the kubelet reports it. */
    state: string;
    tone: Tone;
    restarts: number;
}

/** A pod's containers in start order (init and sidecars, regular, ephemeral), each with its image and current state. */
export function podContainers(obj: Obj | null): PodContainer[] {
    if (!obj) return [];
    const statuses = new Map<string, Obj>();
    for (const key of ['initContainerStatuses', 'containerStatuses', 'ephemeralContainerStatuses']) {
        const list = get(obj, `status.${key}`);
        if (Array.isArray(list)) for (const s of list) if (isObj(s) && typeof s.name === 'string') statuses.set(s.name, s);
    }
    const out: PodContainer[] = [];
    const add = (list: unknown, kind: (c: Obj) => PodContainer['kind']) => {
        if (!Array.isArray(list)) return;
        for (const c of list) {
            if (!isObj(c) || typeof c.name !== 'string') continue;
            const s = statuses.get(c.name);
            const k = kind(c);
            const { state, tone } = containerState(s, k);
            out.push({ name: c.name, kind: k, image: str(c.image), state, tone, restarts: Number(s?.restartCount) || 0 });
        }
    };
    add(get(obj, 'spec.initContainers'), c => (c.restartPolicy === 'Always' ? 'sidecar' : 'init'));
    add(get(obj, 'spec.containers'), () => '');
    add(get(obj, 'spec.ephemeralContainers'), () => 'ephemeral');
    return out;
}

function containerState(s: Obj | undefined, kind: PodContainer['kind']): { state: string; tone: Tone } {
    const st = s?.state;
    if (!isObj(st)) return { state: '', tone: 'neutral' };
    if (isObj(st.running)) {
        // A running container that fails its readiness probe is up but not serving.
        const ready = s?.ready === true || kind === 'ephemeral' || kind === 'init';
        return { state: 'Running', tone: ready ? 'success' : 'warning' };
    }
    if (isObj(st.waiting)) {
        const reason = str(st.waiting.reason) || 'Waiting';
        const bad = /BackOff|Err|Invalid|Failed/i.test(reason);
        return { state: reason, tone: bad ? 'danger' : 'warning' };
    }
    if (isObj(st.terminated)) {
        const code = Number(st.terminated.exitCode) || 0;
        const reason = str(st.terminated.reason) || (code === 0 ? 'Completed' : 'Error');
        return { state: code === 0 ? reason : `${reason} (${code})`, tone: code === 0 ? 'info' : 'danger' };
    }
    return { state: '', tone: 'neutral' };
}
