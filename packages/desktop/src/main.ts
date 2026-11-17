import { app, BrowserWindow, ipcMain, net, protocol, session, shell } from 'electron';
import { existsSync, statSync } from 'node:fs';
import { extname, join, normalize, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

import { AgentSupervisor } from './agent.ts';

const SCHEME = 'app';
const APP_HOST = 'v8s';
const APP_ORIGIN = `${SCHEME}://${APP_HOST}`;

// Dev: Vite's dev server. Otherwise the built UI served through app://v8s, a standard secure scheme, so the page has a real,
// checkable `Origin` header (`app://v8s`) that the agent allow-lists, unlike file:// whose origin is "null".
const DEV_URL = process.env.V8S_DEV_URL;
const ORIGIN = DEV_URL ? new URL(DEV_URL).origin : APP_ORIGIN;

const CSP = [
    "default-src 'self'",
    "script-src 'self'",
    "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
    'font-src https://fonts.gstatic.com',
    "img-src 'self' data:",
    "connect-src 'self' ws://127.0.0.1:*",
    "worker-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "frame-ancestors 'none'"
].join('; ');

protocol.registerSchemesAsPrivileged([{ scheme: SCHEME, privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true } }]);

const uiRoot = app.isPackaged ? join(process.resourcesPath, 'ui') : resolve(__dirname, '../../ui/dist');
const agentSrcDir = resolve(__dirname, '../../../agent');
const bundledAgentDir = join(process.resourcesPath ?? '', 'agent');

const log = (msg: string) => console.log(`[v8s] ${msg}`);

let win: BrowserWindow | null = null;
let quitting = false;

const supervisor = new AgentSupervisor({
    origin: ORIGIN,
    agentSrcDir,
    bundledBin: bundledAgentDir,
    log,
    onChange(info) {
        // A restarted agent has a new port and token; reloading makes the renderer fetch them again.
        if (info && win && !win.isDestroyed() && win.webContents.getURL()) win.webContents.reload();
    }
});

function isTrustedSender(url: string): boolean {
    try {
        // URL.origin is "null" for custom schemes, so compare scheme + host directly.
        const u = new URL(url);
        return `${u.protocol}//${u.host}` === ORIGIN;
    } catch {
        return false;
    }
}

function registerAppProtocol() {
    protocol.handle(SCHEME, async req => {
        const url = new URL(req.url);
        if (url.host !== APP_HOST) return new Response('not found', { status: 404 });
        const rel = normalize(decodeURIComponent(url.pathname)).replace(/^[/\\]+/, '');
        let file = resolve(uiRoot, rel);
        // Block path traversal out of the UI root.
        if (file !== uiRoot && !file.startsWith(uiRoot + sep)) return new Response('forbidden', { status: 403 });
        // SPA fallback: extensionless paths are client routes.
        if (!extname(file) || !existsSync(file) || statSync(file).isDirectory()) {
            if (extname(file)) return new Response('not found', { status: 404 });
            file = join(uiRoot, 'index.html');
        }
        const res = await net.fetch(pathToFileURL(file).toString());
        const headers = new Headers(res.headers);
        headers.set('Content-Security-Policy', CSP);
        headers.set('X-Content-Type-Options', 'nosniff');
        return new Response(res.body, { status: res.status, headers });
    });
}

function createWindow() {
    win = new BrowserWindow({
        width: 1440,
        height: 900,
        minWidth: 480,
        minHeight: 480,
        backgroundColor: '#0c0d10',
        title: 'v8s',
        show: false,
        webPreferences: {
            preload: join(__dirname, 'preload.cjs'),
            contextIsolation: true,
            sandbox: true,
            nodeIntegration: false,
            webSecurity: true
        }
    });
    win.once('ready-to-show', () => win?.show());
    win.on('closed', () => (win = null));

    // Never navigate the app window away from the app origin; external links open in the system browser.
    win.webContents.setWindowOpenHandler(({ url }) => {
        if (url.startsWith('https://')) void shell.openExternal(url);
        return { action: 'deny' };
    });
    win.webContents.on('will-navigate', (e, url) => {
        if (!isTrustedSender(url)) e.preventDefault();
    });

    void win.loadURL(DEV_URL ?? `${APP_ORIGIN}/`);
}

app.on('second-instance', () => {
    if (win) {
        if (win.isMinimized()) win.restore();
        win.focus();
    }
});

if (!app.requestSingleInstanceLock()) {
    app.quit();
} else {
    void app.whenReady().then(() => {
        session.defaultSession.setPermissionRequestHandler((_wc, _perm, cb) => cb(false));
        ipcMain.handle('v8s:get-agent', async e => {
            if (!isTrustedSender(e.senderFrame?.url ?? '')) throw new Error('untrusted sender');
            return await supervisor.whenReady();
        });
        if (!DEV_URL) registerAppProtocol();
        supervisor.start();
        createWindow();
        app.on('activate', () => {
            if (BrowserWindow.getAllWindows().length === 0) createWindow();
        });
    });

    app.on('window-all-closed', () => {
        if (process.platform !== 'darwin') app.quit();
    });

    app.on('before-quit', e => {
        if (quitting) return;
        quitting = true;
        // Hold the quit until the agent is gone so it never outlives the app.
        e.preventDefault();
        void supervisor.stop().finally(() => app.quit());
    });
}
