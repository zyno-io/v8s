import type { ChildProcess } from 'node:child_process';

import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

export interface AgentInfo {
    url: string;
    token: string;
}

export interface SupervisorOptions {
    /** `Origin` the UI is served from; passed to the agent as --allowed-origin. */
    origin: string;
    /** Repo's agent directory for `go run` in dev. */
    agentSrcDir: string;
    /** Packaged agent binary location (extraResources), if any. */
    bundledBin: string;
    onChange(info: AgentInfo | null): void;
    log(msg: string): void;
}

const MIN_DELAY = 500;
const MAX_DELAY = 15_000;
const STABLE_MS = 30_000;
const KILL_GRACE_MS = 3_000;

/** Spawns the agent, reads `{port, token}` from stdout, restarts it with exponential backoff, and kills it on stop(). */
export class AgentSupervisor {
    private child: ChildProcess | null = null;
    private stopped = false;
    private delay = MIN_DELAY;
    private timer: NodeJS.Timeout | null = null;
    private info: AgentInfo | null = null;
    private waiters: ((i: AgentInfo) => void)[] = [];

    constructor(private opts: SupervisorOptions) {}

    start(): void {
        this.spawnOnce();
    }

    /** Resolves with the current agent endpoint, waiting for (re)start if needed. */
    whenReady(): Promise<AgentInfo> {
        if (this.info) return Promise.resolve(this.info);
        return new Promise(res => this.waiters.push(res));
    }

    private command(): { cmd: string; args: string[]; cwd?: string } {
        const common = ['agent', '--stdin-lifeline', '--listen', '127.0.0.1:0', '--allowed-origin', this.opts.origin];
        const bin = process.env.V8S_AGENT_BIN;
        if (bin) return { cmd: bin, args: common };
        const exe = join(this.opts.bundledBin, process.platform === 'win32' ? 'v8s.exe' : 'v8s');
        if (existsSync(exe)) return { cmd: exe, args: common };
        return { cmd: 'go', args: ['run', './cmd/v8s', ...common], cwd: this.opts.agentSrcDir };
    }

    private spawnOnce(): void {
        if (this.stopped) return;
        const { cmd, args, cwd } = this.command();
        this.opts.log(`starting agent: ${cmd} ${args.join(' ')}`);
        const startedAt = Date.now();
        const child = spawn(cmd, args, { cwd, stdio: ['pipe', 'pipe', 'inherit'] });
        this.child = child;

        let buf = '';
        let announced = false;
        child.stdout!.on('data', (d: Buffer) => {
            if (announced) return;
            buf += d.toString();
            const line = buf.split('\n').find(l => l.startsWith('{'));
            if (!line) return;
            try {
                const { port, token } = JSON.parse(line) as { port: number; token: string };
                announced = true;
                this.info = { url: `ws://127.0.0.1:${port}/ws`, token };
                this.opts.onChange(this.info);
                for (const w of this.waiters.splice(0)) w(this.info);
            } catch (err) {
                this.opts.log(`unparseable agent banner: ${String(err)}`);
            }
        });

        child.once('error', err => this.opts.log(`agent spawn error: ${err.message}`));
        child.once('exit', (code, signal) => {
            if (this.child === child) this.child = null;
            if (this.stopped) return;
            this.opts.log(`agent exited (code=${code} signal=${signal})`);
            if (this.info) {
                this.info = null;
                this.opts.onChange(null);
            }
            if (Date.now() - startedAt > STABLE_MS) this.delay = MIN_DELAY;
            const wait = this.delay;
            this.delay = Math.min(this.delay * 2, MAX_DELAY);
            this.opts.log(`restarting agent in ${wait} ms`);
            this.timer = setTimeout(() => this.spawnOnce(), wait);
        });
    }

    /** Stops restarting and terminates the agent: close the lifeline first, then SIGTERM, then SIGKILL. */
    async stop(): Promise<void> {
        this.stopped = true;
        if (this.timer) clearTimeout(this.timer);
        const child = this.child;
        if (!child || child.exitCode !== null) return;
        await new Promise<void>(res => {
            const kill = setTimeout(() => child.kill('SIGKILL'), KILL_GRACE_MS);
            child.once('exit', () => {
                clearTimeout(kill);
                res();
            });
            child.stdin?.end();
            child.kill('SIGTERM');
        });
    }
}
