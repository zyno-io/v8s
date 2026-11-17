import type { Flow, FlowEndpoint, FlowFilter, FlowRate, FlowScope, ObjectRef } from '@v8s/client';

// Pure helpers behind FlowsPane / FlowTable (no Vue, no DOM) so they can be unit tested.

// ---- scope ----

const WORKLOAD_GVRS = new Set([
    'apps/v1/deployments',
    'apps/v1/statefulsets',
    'apps/v1/daemonsets',
    'apps/v1/replicasets',
    'batch/v1/jobs',
    'v1/services'
]);

/** The server-side scope of an object's flows: a pod, a workload (a Service uses its own name), or a whole namespace. Null: no flows. */
export function flowScope(target: Pick<ObjectRef, 'gvr' | 'namespace' | 'name'>): FlowScope | null {
    const name = target.name ?? '';
    if (target.gvr === 'v1/namespaces') return name ? { namespace: name } : null;
    const ns = target.namespace ?? '';
    if (!ns || !name) return null;
    if (target.gvr === 'v1/pods') return { namespace: ns, pod: name };
    if (WORKLOAD_GVRS.has(target.gvr)) return { namespace: ns, workload: name };
    return null;
}

// ---- filter ----

export interface FlowFilterState {
    verdict: string; // '' | FORWARDED | DROPPED | ERROR | AUDIT
    direction: string; // '' | INGRESS | EGRESS
    protocol: string; // '' | tcp | udp | icmp | http | dns
    dropReason: string;
    httpStatus: string;
    dnsQuery: string;
}

export const emptyFilter = (): FlowFilterState => ({ verdict: '', direction: '', protocol: '', dropReason: '', httpStatus: '', dnsQuery: '' });

export const VERDICTS = ['FORWARDED', 'DROPPED', 'ERROR', 'AUDIT'] as const;
export const PROTOCOLS = ['tcp', 'udp', 'icmp', 'http', 'dns'] as const;

/** A verdict from a URL query (`?fv=DROPPED`); anything unknown is ignored. */
export function verdictFromQuery(v: unknown): string {
    const s = (Array.isArray(v) ? v[0] : v) as unknown;
    if (typeof s !== 'string') return '';
    const u = s.toUpperCase();
    return (VERDICTS as readonly string[]).includes(u) ? u : '';
}

/** Which optional inputs apply: the reason only for dropped/all verdicts, the HTTP status for http, the DNS query for dns. */
export function relevantInputs(f: FlowFilterState): { dropReason: boolean; httpStatus: boolean; dnsQuery: boolean } {
    return {
        dropReason: f.verdict === '' || f.verdict === 'DROPPED',
        httpStatus: f.protocol === 'http',
        dnsQuery: f.protocol === 'dns'
    };
}

/** FlowFilter params for the stream: empty and irrelevant fields are omitted. */
export function toFlowFilter(f: FlowFilterState): FlowFilter {
    const rel = relevantInputs(f);
    const out: FlowFilter = {};
    if (f.verdict) out.verdict = f.verdict;
    if (f.direction) out.direction = f.direction;
    if (f.protocol) out.protocol = f.protocol;
    const reason = f.dropReason.trim();
    if (reason && rel.dropReason) out.dropReason = reason.toUpperCase();
    const status = f.httpStatus.trim();
    if (status && rel.httpStatus) out.httpStatus = status;
    const dns = f.dnsQuery.trim();
    if (dns && rel.dnsQuery) out.dnsQuery = dns;
    return out;
}

/** Stable key of what the relay is asked for: the stream reopens exactly when this changes. */
export const filterKey = (f: FlowFilterState): string => JSON.stringify(toFlowFilter(f));

export const FILTER_DEBOUNCE_MS = 300;

/** Whether a change reopens the stream: text inputs wait for a pause in typing, a select change reopens at once. */
export function reopenDecision(prev: FlowFilterState, next: FlowFilterState): 'none' | 'debounced' | 'now' {
    const a = toFlowFilter(prev);
    const b = toFlowFilter(next);
    const texts = ['dropReason', 'httpStatus', 'dnsQuery'] as const;
    if (texts.some(k => a[k] !== b[k])) return 'debounced';
    return filterKey(prev) === filterKey(next) ? 'none' : 'now';
}

// ---- sizes and rate ----

export const CAPACITY_DESKTOP = 10000;
export const CAPACITY_PHONE = 2000;
export const flowCapacity = (phone: boolean): number => (phone ? CAPACITY_PHONE : CAPACITY_DESKTOP);

export const ROW_H_FLOW = 30;
export const ROW_H_FLOW_PHONE = 72;
export const FLOW_VIEW_PAD = 40;

/** "showing 200/s of ~950/s, narrow the filter" when sampled, else "12 flows/s". Empty before the first report. */
export function rateText(rate: FlowRate | null | undefined): string {
    if (!rate) return '';
    if (rate.sampled) return `showing ${rate.shown}/s of ~${rate.total}/s, narrow the filter`;
    return `${rate.total} flows/s`;
}

export const formatNewFlows = (n: number): string => `${n.toLocaleString('en-US')} new`;

// ---- rows ----

const pad = (n: number, w: number): string => String(n).padStart(w, '0');

/** HH:MM:SS.mmm in local time. */
export function flowTime(ms: number): string {
    if (!ms) return '';
    const d = new Date(ms);
    return `${pad(d.getHours(), 2)}:${pad(d.getMinutes(), 2)}:${pad(d.getSeconds(), 2)}.${pad(d.getMilliseconds(), 3)}`;
}

/** `workload` (or pod, or IP) plus namespace and port, split so the namespace can be muted. */
export function endpointParts(e: FlowEndpoint): { name: string; ns: string; port: string } {
    return { name: e.workload || e.pod || e.ip || '?', ns: e.namespace ?? '', port: e.port ? String(e.port) : '' };
}

export function endpointText(e: FlowEndpoint): string {
    const p = endpointParts(e);
    return `${p.ns ? p.ns + '/' : ''}${p.name}${p.port ? ':' + p.port : ''}`;
}

/** Destination port and protocol such as `8080/TCP`. */
export function portProto(f: Pick<Flow, 'dst' | 'protocol'>): string {
    const port = f.dst.port ? String(f.dst.port) : '';
    return [port, f.protocol ?? ''].filter(Boolean).join('/');
}

export type VerdictTone = 'success' | 'danger' | 'info';
export function verdictTone(v: string): VerdictTone {
    if (v === 'FORWARDED') return 'success';
    if (v === 'DROPPED' || v === 'ERROR') return 'danger';
    return 'info';
}
export const isDropped = (v: string): boolean => v === 'DROPPED';

// ---- open errors ----

/** The agent says there is no usable relay with these codes. */
export const NO_RELAY_CODES = new Set(['unavailable', 'not_found', 'bad_request']);
export function noRelay(e: unknown): { message: string; hint: string } | null {
    const err = e as { code?: string; message?: string; hint?: string } | null;
    if (!err || typeof err.code !== 'string' || !NO_RELAY_CODES.has(err.code)) return null;
    return { message: err.message ?? 'No Hubble relay in this cluster', hint: err.hint ?? '' };
}
