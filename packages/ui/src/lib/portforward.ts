import type { ObjectRef, PortForwardInfo, PortForwardListResult, SavedForward } from '@v8s/client';

import { shallowRef } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { isPhone } from '@/lib/viewport.ts';
import { withWriteConfirm } from '@/lib/write-confirm.ts';
import { useSettingsStore } from '@/stores/settings.ts';

/** Set to open the port-forward dialog for a pod or service (read by PortForwardHost). */
export const portForwardRequest = shallowRef<ObjectRef | null>(null);
/** Whether the active-forwards panel is open. */
export const portForwardsOpen = shallowRef(false);

export interface PortOption {
    port: number;
    name?: string;
    protocol?: string;
    containerName?: string;
}

export interface StartResult {
    localPort: number;
    forwardId: string;
    usedContext?: string;
}

/** Port forwarding needs a local listener: desktop app and `v8s serve` only. */
export function portForwardAvailable(): boolean {
    if (isPhone.value) return false; // a local port is meaningless on a phone
    try {
        const mode = getAgent().agentInfo.value?.mode;
        return mode === 'desktop' || mode === 'serve' || mode === 'fake';
    } catch {
        return false;
    }
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);

/** Ports declared by a pod (containerPort) or a service (port). */
export function portsOf(gvr: string, parsed: Record<string, unknown> | null | undefined): PortOption[] {
    const spec = parsed && isObj(parsed.spec) ? parsed.spec : null;
    if (!spec) return [];
    const out: PortOption[] = [];
    const seen = new Set<string>();
    const add = (o: PortOption) => {
        const k = `${o.port}/${o.protocol ?? 'TCP'}`;
        if (seen.has(k)) return;
        seen.add(k);
        out.push(o);
    };
    if (gvr === 'v1/pods') {
        for (const c of Array.isArray(spec.containers) ? spec.containers : []) {
            if (!isObj(c)) continue;
            for (const p of Array.isArray(c.ports) ? c.ports : []) {
                if (!isObj(p) || typeof p.containerPort !== 'number') continue;
                add({
                    port: p.containerPort,
                    name: typeof p.name === 'string' ? p.name : undefined,
                    protocol: typeof p.protocol === 'string' ? p.protocol : undefined,
                    containerName: typeof c.name === 'string' ? c.name : undefined
                });
            }
        }
    } else if (gvr === 'v1/services') {
        for (const p of Array.isArray(spec.ports) ? spec.ports : []) {
            if (!isObj(p) || typeof p.port !== 'number') continue;
            add({
                port: p.port,
                name: typeof p.name === 'string' ? p.name : undefined,
                protocol: typeof p.protocol === 'string' ? p.protocol : undefined
            });
        }
    }
    return out;
}

const HTTP_PORTS = new Set([80, 443, 3000, 3001, 4200, 5000, 5173, 8000, 8001, 8080, 8081, 8443, 8888, 9000, 9090]);

/** Heuristic: should this port offer an "Open in browser" link? */
export function isHttpish(p: { port: number; name?: string; protocol?: string }): boolean {
    if (p.protocol && p.protocol.toUpperCase() !== 'TCP') return false;
    if (p.name && /http|web|ui/i.test(p.name)) return true;
    return HTTP_PORTS.has(p.port);
}

export const browserUrl = (localPort: number): string => `http://127.0.0.1:${localPort}`;

export const forwardKey = (ref: ObjectRef, port: number): string => `${ref.cluster}|${ref.gvr}|${ref.namespace ?? ''}|${ref.name ?? ''}|${port}`;

export function startForward(ref: ObjectRef, port: number): Promise<StartResult> {
    return withWriteConfirm(ref.cluster, extra => getAgent().call<StartResult>('stream.open', { type: 'portforward', ref, port, ...extra }));
}

export async function listForwards(): Promise<PortForwardInfo[]> {
    const res = await getAgent().call<PortForwardListResult>('portforward.list', {});
    return res.forwards ?? [];
}

export async function stopForward(id: string): Promise<void> {
    await getAgent().call('portforward.stop', { id });
}

export async function saveForward(f: { name?: string; ref: ObjectRef; port: number }): Promise<void> {
    const settings = useSettingsStore();
    const cur = settings.doc.portForwards ?? [];
    const key = forwardKey(f.ref, f.port);
    const next: SavedForward[] = [
        ...cur.filter(s => forwardKey(s.ref, s.port) !== key),
        { ...(f.name ? { name: f.name } : {}), ref: f.ref, port: f.port }
    ];
    await settings.patch({ portForwards: next });
}

export async function removeSavedForward(index: number): Promise<void> {
    const settings = useSettingsStore();
    const cur = settings.doc.portForwards ?? [];
    await settings.patch({ portForwards: cur.filter((_, i) => i !== index) });
}

/** Active forward matching a saved one (same target and port), if any. */
export function savedMatches(saved: SavedForward, active: readonly PortForwardInfo[]): PortForwardInfo | undefined {
    const key = forwardKey(saved.ref, saved.port);
    return active.find(a => a.state !== 'stopped' && forwardKey(a.ref, a.port) === key);
}

export function describeState(state: string): { tone: 'success' | 'warning' | 'neutral'; text: string } {
    if (state === 'running') return { tone: 'success', text: 'running' };
    if (state === 'reconnecting') return { tone: 'warning', text: 'reconnecting' };
    return { tone: 'neutral', text: state || 'stopped' };
}
