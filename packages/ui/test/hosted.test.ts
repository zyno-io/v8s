import { describe, expect, it } from 'vitest';

import {
    clusterLoginUrl,
    expiryMs,
    hostedWsUrl,
    loginUrl,
    LOGIN_LOOP_MS,
    logout,
    mayRedirectToLogin,
    probeHosted,
    safeReturn,
    sessionEnded
} from '../src/lib/hosted.ts';

const loc = { protocol: 'https:', host: 'v8s.example.com', pathname: '/r/apps/v1/deployments', search: '?c=prod' };
const reply = (status: number, body?: unknown) =>
    (async () => new Response(body === undefined ? null : JSON.stringify(body), { status })) as unknown as typeof fetch;

describe('hosted bootstrap', () => {
    it('401 redirects to the login with the current path and query as return', async () => {
        const r = await probeHosted(reply(401), loc);
        expect(r).toEqual({ kind: 'login', url: '/auth/login?return=' + encodeURIComponent('/r/apps/v1/deployments?c=prod') });
    });

    it('200 connects over wss on the same origin without a token', async () => {
        const r = await probeHosted(reply(200, { username: 'ada', clusterAuth: 'required', expiresAt: 1900000000 }), loc);
        expect(r.kind).toBe('ok');
        if (r.kind !== 'ok') return;
        expect(r.endpoint).toEqual({ url: 'wss://v8s.example.com/ws', token: '' });
        expect(r.me.clusterAuth).toBe('required');
    });

    it('404, a network error or a non-me body is "not hosted"', async () => {
        expect((await probeHosted(reply(404), loc)).kind).toBe('none');
        expect((await probeHosted((async () => Promise.reject(new Error('x'))) as unknown as typeof fetch, loc)).kind).toBe('none');
        expect((await probeHosted(reply(200, { nope: 1 }), loc)).kind).toBe('none');
    });

    it('never redirects to an off-site return path', () => {
        expect(safeReturn('//evil.example/x')).toBe('/');
        expect(safeReturn('https://evil.example')).toBe('/');
        expect(loginUrl('//evil.example')).toBe('/auth/login?return=%2F');
        expect(clusterLoginUrl('/pods?x=1')).toBe('/auth/cluster-login?return=' + encodeURIComponent('/pods?x=1'));
    });

    it('builds ws/wss urls from the page origin', () => {
        expect(hostedWsUrl({ protocol: 'http:', host: 'localhost:8080' })).toBe('ws://localhost:8080/ws');
    });

    it('reads the session expiry in seconds, milliseconds or RFC 3339', () => {
        expect(expiryMs(1900000000)).toBe(1900000000000);
        expect(expiryMs(1900000000000)).toBe(1900000000000);
        expect(expiryMs('2030-01-01T00:00:00Z')).toBe(Date.parse('2030-01-01T00:00:00Z'));
        expect(expiryMs(undefined)).toBe(0);
    });

    it('a closed socket with a 401 from /api/me means the session ended', async () => {
        expect(await sessionEnded(reply(401))).toBe(true);
        expect(await sessionEnded(reply(200, {}))).toBe(false);
    });

    it('logout POSTs then lands on the signed-out page (not an auto sign-in)', async () => {
        const calls: string[] = [];
        const f = (async (url: string, init?: RequestInit) => {
            calls.push(`${init?.method} ${url}`);
            return new Response(null, { status: 204 });
        }) as unknown as typeof fetch;
        let to = '';
        await logout(f, u => (to = u));
        expect(calls).toEqual(['POST /auth/logout']);
        expect(to).toBe('/?signedout=1');
    });
});

describe('login loop guard', () => {
    const mem = () => {
        const m = new Map<string, string>();
        return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
    };
    it('allows one redirect, then refuses a second within the window, then allows again', () => {
        const st = mem();
        expect(mayRedirectToLogin(st, 1_000_000)).toBe(true);
        expect(mayRedirectToLogin(st, 1_000_000 + LOGIN_LOOP_MS - 1)).toBe(false);
        expect(mayRedirectToLogin(st, 1_000_000 + LOGIN_LOOP_MS + 1)).toBe(true);
    });
    it('without storage there is no guard', () => {
        expect(mayRedirectToLogin(null, 5)).toBe(true);
    });
});
