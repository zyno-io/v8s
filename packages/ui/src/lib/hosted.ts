import type { AgentEndpoint } from '@v8s/client';

import { shallowRef } from 'vue';

/** `GET /api/me`: the signed-in user of a hosted (`v8s server`) session. */
export interface HostedMe {
    username: string;
    sub?: string;
    groups?: string[];
    /** Unix seconds (or ms) when the session ends; see `expiryMs`. */
    expiresAt?: number | string;
    admin?: boolean;
    clusterAuth: 'ok' | 'required';
}

export type HostedProbe =
    | { kind: 'ok'; me: HostedMe; endpoint: AgentEndpoint }
    | { kind: 'login'; url: string }
    // Not a hosted server (404, network error, a non-JSON answer): the caller falls back to "no endpoint".
    | { kind: 'none' };

/** The signed-in hosted user, null on desktop/serve/fake. Replaced when the account menu refreshes it. */
export const hostedMe = shallowRef<HostedMe | null>(null);

/** `/auth/login?return=<path>`; `return` is always an in-app path (a single leading slash). */
export function loginUrl(returnPath: string): string {
    return '/auth/login?return=' + encodeURIComponent(safeReturn(returnPath));
}

export function clusterLoginUrl(returnPath: string): string {
    return '/auth/cluster-login?return=' + encodeURIComponent(safeReturn(returnPath));
}

/** Only in-app paths: no protocol-relative or absolute URLs, so a crafted `return` cannot become an open redirect. */
export function safeReturn(p: string): string {
    return p.startsWith('/') && !p.startsWith('//') && !p.startsWith('/\\') ? p : '/';
}

/** `wss://host/ws` for the page's own origin (hosted serves the UI and the socket from one origin). */
export function hostedWsUrl(loc: Pick<Location, 'protocol' | 'host'>): string {
    return (loc.protocol === 'https:' ? 'wss://' : 'ws://') + loc.host + '/ws';
}

/** Session expiry as epoch milliseconds (accepts seconds, milliseconds or an RFC 3339 string); 0 when unknown. */
export function expiryMs(v: HostedMe['expiresAt']): number {
    if (v === undefined || v === '') return 0;
    if (typeof v === 'string') {
        const t = Date.parse(v);
        if (Number.isFinite(t)) return t;
        v = Number(v);
    }
    if (!Number.isFinite(v) || v <= 0) return 0;
    return v < 1e12 ? v * 1000 : v;
}

/**
 * Hosted bootstrap: asks `/api/me`. 200 → hosted, connect with the cookie (no token); 401 → send the browser to the login
 * (the callback returns to the current path); anything else → not a hosted server.
 */
export async function probeHosted(fetchFn: typeof fetch, loc: Pick<Location, 'protocol' | 'host' | 'pathname' | 'search'>): Promise<HostedProbe> {
    let res: Response;
    try {
        res = await fetchFn('/api/me', { credentials: 'same-origin', headers: { Accept: 'application/json' } });
    } catch {
        return { kind: 'none' };
    }
    if (res.status === 401) return { kind: 'login', url: loginUrl(loc.pathname + loc.search) };
    if (!res.ok) return { kind: 'none' };
    try {
        const me = (await res.json()) as HostedMe;
        if (!me || typeof me !== 'object' || typeof me.username !== 'string') return { kind: 'none' };
        return {
            kind: 'ok',
            me: { ...me, clusterAuth: me.clusterAuth === 'required' ? 'required' : 'ok' },
            endpoint: { url: hostedWsUrl(loc), token: '' }
        };
    } catch {
        return { kind: 'none' };
    }
}

/** True when the session behind a failed or closed socket has ended (`/api/me` answers 401): the caller then redirects to the login. */
export async function sessionEnded(fetchFn: typeof fetch): Promise<boolean> {
    try {
        return (await fetchFn('/api/me', { credentials: 'same-origin' })).status === 401;
    } catch {
        return false;
    }
}

/** Where logout lands: `/` with a marker, so the boot shows "signed out" instead of silently signing back in through the IdP's SSO session. */
export const SIGNED_OUT_URL = '/?signedout=1';

/** `POST /auth/logout`, then the signed-out page. The server answers 204 and clears the cookie; a failed request still leaves the page. */
/** True while a logout is in flight: the closing socket must not bounce the browser back to the login. */
export const logoutState = { active: false };

export async function logout(fetchFn: typeof fetch, navigate: (url: string) => void): Promise<void> {
    logoutState.active = true;
    try {
        await fetchFn('/auth/logout', { method: 'POST', credentials: 'same-origin' });
    } catch {
        // leave anyway: the session ends on its own expiry
    }
    navigate(SIGNED_OUT_URL);
}

/** "in 3 h 20 min", "in 12 min", "expired": the account menu's session countdown. */
export function sessionLeft(expiresMs: number, nowMs: number): string {
    if (!expiresMs) return '';
    const m = Math.floor((expiresMs - nowMs) / 60000);
    if (m <= 0) return 'expired';
    if (m < 60) return `in ${m} min`;
    const h = Math.floor(m / 60);
    if (h < 48) return `in ${h} h ${m % 60} min`;
    return `in ${Math.floor(h / 24)} d`;
}

const LOGIN_KEY = 'v8s.loginAt';
/** A login round trip that comes back to a 401 within this window means the session cookie is not being kept. */
export const LOGIN_LOOP_MS = 20_000;

/**
 * Guards the 401 → login → 401 loop (cookies blocked, a proxy stripping them): true when the browser may be sent to the login now,
 * false when it was sent less than `LOGIN_LOOP_MS` ago, so the boot shows an error instead of hammering the IdP.
 */
export function mayRedirectToLogin(storage: Pick<Storage, 'getItem' | 'setItem'> | null, now: number): boolean {
    try {
        const last = Number(storage?.getItem(LOGIN_KEY) ?? 0);
        if (last && now - last < LOGIN_LOOP_MS) return false;
        storage?.setItem(LOGIN_KEY, String(now));
    } catch {
        // no storage: no guard
    }
    return true;
}
