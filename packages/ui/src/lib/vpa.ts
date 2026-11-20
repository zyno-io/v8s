/** VerticalPodAutoscaler helpers: Kubernetes quantities, a VPA's recommendations, a pod's requests, the `vpaUpdates` annotation. */

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => !!v && typeof v === 'object' && !Array.isArray(v);
const get = (o: unknown, path: string): unknown => path.split('.').reduce<unknown>((a, k) => (isObj(a) ? a[k] : undefined), o);

const SUFFIX: Record<string, number> = {
    n: 1e-9,
    u: 1e-6,
    m: 1e-3,
    '': 1,
    k: 1e3,
    M: 1e6,
    G: 1e9,
    T: 1e12,
    P: 1e15,
    E: 1e18,
    Ki: 1024,
    Mi: 1024 ** 2,
    Gi: 1024 ** 3,
    Ti: 1024 ** 4,
    Pi: 1024 ** 5,
    Ei: 1024 ** 6
};

/** A Kubernetes quantity (`250m`, `1.5Gi`, `1e3`, `129e6`, `12E`) in base units, or null when it does not parse. */
export function parseQuantity(q: unknown): number | null {
    if (typeof q === 'number') return Number.isFinite(q) ? q : null;
    if (typeof q !== 'string') return null;
    const m = /^([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)([a-zA-Z]*)$/.exec(q.trim());
    if (!m || !(m[2] in SUFFIX)) return null;
    const v = Number(m[1]) * SUFFIX[m[2]];
    return Number.isFinite(v) ? v : null;
}

/** CPU quantity to millicores, rounded up. */
export function cpuMillis(q: unknown): number | null {
    const v = parseQuantity(q);
    // Round away float noise (0.1 * 1000) before the ceil.
    return v == null ? null : Math.ceil(+(v * 1000).toFixed(6));
}

/** Memory quantity to MiB, rounded down. */
export function memMiB(q: unknown): number | null {
    const v = parseQuantity(q);
    return v == null ? null : Math.floor(+(v / 1024 ** 2).toFixed(6));
}

/** `250m`, or cores from a full core up (`1.5 cores`). */
export const cpuShort = (m: number | null): string => (m == null ? '—' : m >= 1000 ? `${+(m / 1000).toFixed(2)} cores` : `${m}m`);
/** `512Mi`, or `1.5Gi` from a GiB up. */
export const memShort = (mi: number | null): string => (mi == null ? '—' : mi >= 1024 ? `${+(mi / 1024).toFixed(2)}Gi` : `${mi}Mi`);

export interface Res {
    cpu: number | null;
    mem: number | null;
}

export interface Recommendation {
    container: string;
    target: Res;
    lower: Res;
    upper: Res;
    uncapped: Res;
}

const res = (o: unknown): Res => ({ cpu: cpuMillis(get(o, 'cpu')), mem: memMiB(get(o, 'memory')) });

/** The per-container recommendations of a parsed VPA (empty before the recommender has produced any). */
export function vpaRecommendations(vpa: Obj | null): Recommendation[] {
    const raw = get(vpa, 'status.recommendation.containerRecommendations');
    if (!Array.isArray(raw)) return [];
    return raw.filter(isObj).map(r => ({
        container: String(r.containerName ?? ''),
        target: res(r.target),
        lower: res(r.lowerBound),
        upper: res(r.upperBound),
        uncapped: res(r.uncappedTarget)
    }));
}

export interface ContainerRequests extends Res {
    container: string;
}

/** The requests of a parsed pod's containers, in spec order. */
export function podRequests(pod: Obj | null): ContainerRequests[] {
    const raw = get(pod, 'spec.containers');
    if (!Array.isArray(raw)) return [];
    return raw.filter(isObj).map(c => ({ container: String(c.name ?? ''), ...res(get(c, 'resources.requests')) }));
}

export interface VpaUpdates {
    name: string;
    /** What the admission controller changed, as written after the name (`container 0: cpu request, memory request`). */
    updated: string;
    /** The same per container: key is the container index or name as written. */
    perContainer: Record<string, string>;
}

const PREFIX = 'Pod resources updated by ';

/** Parses the pod annotation `vpaUpdates` (`Pod resources updated by <name>: container 0: cpu request, memory request`); null when it is not one. */
export function vpaNameFromAnnotation(value: string | undefined | null): VpaUpdates | null {
    if (!value || !value.startsWith(PREFIX)) return null;
    const rest = value.slice(PREFIX.length);
    const i = rest.indexOf(':');
    const name = (i < 0 ? rest : rest.slice(0, i)).trim();
    if (!name) return null;
    const updated = i < 0 ? '' : rest.slice(i + 1).trim();
    const perContainer: Record<string, string> = {};
    for (const part of updated.split(';')) {
        const m = /^\s*container\s+(\S+?)\s*:\s*(.*?)\s*$/.exec(part);
        if (m) perContainer[m[1]] = m[2];
    }
    return { name, updated, perContainer };
}

/** `Kind/name` of a VPA's target, or ''. */
export function vpaTarget(vpa: Obj | null): string {
    const kind = get(vpa, 'spec.targetRef.kind');
    const name = get(vpa, 'spec.targetRef.name');
    return typeof kind === 'string' && typeof name === 'string' ? `${kind}/${name}` : '';
}

/** Whether a request sits outside the recommended [lower, upper] range (false when either side is unknown). */
export const outside = (req: number | null, lower: number | null, upper: number | null): boolean =>
    req != null && ((lower != null && req < lower) || (upper != null && req > upper));

/** Whether a gvr is a VerticalPodAutoscaler list (any served version). */
export const isVpaGvr = (gvr: string): boolean => gvr.startsWith('autoscaling.k8s.io/') && gvr.endsWith('/verticalpodautoscalers');
export const VPA_GVR = 'autoscaling.k8s.io/v1/verticalpodautoscalers';
