import type { AgentEndpoint } from '@v8s/client';

const STORAGE_KEY = 'v8s.agent';

/** Parses `agent=ws://127.0.0.1:PORT/ws&token=…` from a URL fragment (without the leading `#`). */
export function parseFragment(fragment: string): AgentEndpoint | null {
    const params = new URLSearchParams(fragment.replace(/^#/, ''));
    const url = params.get('agent');
    const token = params.get('token');
    if (!url || !token) return null;
    if (!/^wss?:\/\//.test(url)) return null;
    return { url, token };
}

function readSession(): AgentEndpoint | null {
    try {
        const raw = sessionStorage.getItem(STORAGE_KEY);
        if (!raw) return null;
        const v = JSON.parse(raw) as AgentEndpoint;
        return v.url && v.token ? v : null;
    } catch {
        return null;
    }
}

function writeSession(ep: AgentEndpoint) {
    try {
        sessionStorage.setItem(STORAGE_KEY, JSON.stringify(ep));
    } catch {
        // storage unavailable: the endpoint only lives for this page load
    }
}

/**
 * Finds the agent endpoint: Electron preload first, then the URL fragment (token moves to sessionStorage and the
 * fragment is cleared so it never lingers in history or screenshots), then a previous sessionStorage entry.
 */
export async function resolveEndpoint(): Promise<AgentEndpoint | null> {
    if (window.v8sDesktop) {
        try {
            return await window.v8sDesktop.getAgent();
        } catch {
            // fall through to the other sources
        }
    }
    const fromHash = parseFragment(location.hash);
    if (fromHash) {
        writeSession(fromHash);
        history.replaceState(null, '', location.pathname + location.search);
        return fromHash;
    }
    return readSession();
}
