import type { ChildProcess } from 'node:child_process';

import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
export const uiDir = resolve(here, '..');
export const repoRoot = resolve(uiDir, '../..');
export const agentDir = join(repoRoot, 'agent');

export interface Stack {
    previewPort: number;
    agentPort: number;
    token: string;
    url: string;
    restartAgent(): Promise<void>;
    stop(): Promise<void>;
}

async function freePort(): Promise<number> {
    return await new Promise((res, rej) => {
        const srv = createServer();
        srv.once('error', rej);
        srv.listen(0, '127.0.0.1', () => {
            const port = (srv.address() as { port: number }).port;
            srv.close(() => res(port));
        });
    });
}

function run(cmd: string, args: string[], cwd: string): Promise<void> {
    return new Promise((res, rej) => {
        const p = spawn(cmd, args, { cwd, stdio: ['ignore', 'inherit', 'inherit'] });
        p.once('error', rej);
        p.once('exit', code => (code === 0 ? res() : rej(new Error(`${cmd} ${args.join(' ')} exited with ${code}`))));
    });
}

async function waitHttp(url: string, timeoutMs = 20000): Promise<void> {
    const start = Date.now();
    for (;;) {
        try {
            const r = await fetch(url);
            if (r.ok) return;
        } catch {
            // not up yet
        }
        if (Date.now() - start > timeoutMs) throw new Error(`timed out waiting for ${url}`);
        await new Promise(r => setTimeout(r, 150));
    }
}

/** Builds and starts the fake-mode Go agent and `vite preview` of the built UI. The caller must have run `vite build`. */
export async function startStack(fake: string, token = 'perf'): Promise<Stack> {
    const previewPort = await freePort();
    // `V8S_AGENT_BIN` points at a prebuilt agent (e.g. one with a newer fake); otherwise build ./cmd/v8s.
    const bin = process.env.V8S_AGENT_BIN || join(tmpdir(), 'v8s-perf', 'v8s');
    if (!process.env.V8S_AGENT_BIN) {
        mkdirSync(dirname(bin), { recursive: true });
        // Build first so the process we start (and kill) is the agent itself, not `go run`'s wrapper.
        await run('go', ['build', '-o', bin, './cmd/v8s'], agentDir);
    }

    const procs: ChildProcess[] = [];
    const viteBin = join(dirname(createRequire(import.meta.url).resolve('vite/package.json')), 'bin/vite.js');
    const preview = spawn(process.execPath, [viteBin, 'preview', '--host', '127.0.0.1', '--port', String(previewPort), '--strictPort'], {
        cwd: uiDir,
        stdio: ['ignore', 'ignore', 'inherit']
    });
    procs.push(preview);

    const startAgent = async (listen: string) => {
        const agent = spawn(
            bin,
            ['agent', '--fake', fake, '--listen', listen, '--dev-token', token, '--allowed-origin', `http://127.0.0.1:${previewPort}`],
            { cwd: agentDir, stdio: ['pipe', 'pipe', 'inherit'] }
        );
        procs.push(agent);
        const port = await new Promise<number>((res, rej) => {
            let buf = '';
            const t = setTimeout(() => rej(new Error('agent did not print its port')), 20000);
            agent.once('exit', code => {
                clearTimeout(t);
                rej(new Error(`agent exited early (${code})`));
            });
            agent.stdout!.on('data', (d: Buffer) => {
                buf += d.toString();
                const line = buf.split('\n').find(l => l.startsWith('{'));
                if (line) {
                    clearTimeout(t);
                    res((JSON.parse(line) as { port: number }).port);
                }
            });
        });
        return { agent, port };
    };
    let current = await startAgent('127.0.0.1:0');
    const agentPort = current.port;
    await waitHttp(`http://127.0.0.1:${previewPort}/`);

    return {
        previewPort,
        agentPort,
        token,
        url: `http://127.0.0.1:${previewPort}/#agent=ws://127.0.0.1:${agentPort}/ws&token=${token}`,
        /** Kills the agent and starts a fresh one on the same port (in-memory state is lost, as in a redeploy). */
        async restartAgent() {
            const old = current.agent;
            old.kill('SIGKILL');
            if (old.exitCode === null && old.signalCode === null) await new Promise(r => old.once('exit', r));
            for (let i = 0; ; i++) {
                try {
                    current = await startAgent(`127.0.0.1:${agentPort}`);
                    return;
                } catch (e) {
                    if (i >= 20) throw e;
                    await new Promise(r => setTimeout(r, 200));
                }
            }
        },
        async stop() {
            for (const p of procs) p.kill('SIGTERM');
            await new Promise(r => setTimeout(r, 300));
            for (const p of procs) if (p.exitCode === null) p.kill('SIGKILL');
        }
    };
}
