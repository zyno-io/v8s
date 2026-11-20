/**
 * A/B perf check: `node perf/ab.ts --base-dir <checkout of the base revision, yarn installed> [--rounds 3] [--out-dir <dir>]`.
 * Builds both revisions, then alternates base and head runs of THIS checkout's `perf/run.ts` (desktop only) against each
 * revision's own UI build and agent, and compares them with `compare.ts`. Exits with compare's exit code.
 */
import { spawn } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

const here = dirname(fileURLToPath(import.meta.url));
const uiDir = resolve(here, '..');
const repoRoot = resolve(uiDir, '../..');

function exec(cmd: string, args: string[], cwd: string, env: NodeJS.ProcessEnv = process.env): Promise<number> {
    console.log(`$ (${cwd}) ${cmd} ${args.join(' ')}`);
    return new Promise((res, rej) => {
        const p = spawn(cmd, args, { cwd, env, stdio: 'inherit' });
        p.once('error', rej);
        p.once('exit', code => res(code ?? 1));
    });
}

async function must(cmd: string, args: string[], cwd: string, env?: NodeJS.ProcessEnv) {
    const code = await exec(cmd, args, cwd, env);
    if (code !== 0) throw new Error(`${cmd} ${args.join(' ')} exited with ${code}`);
}

async function main(): Promise<number> {
    const { values } = parseArgs({
        options: { 'base-dir': { type: 'string' }, rounds: { type: 'string', default: '3' }, 'out-dir': { type: 'string' } }
    });
    if (!values['base-dir']) throw new Error('usage: ab.ts --base-dir <path> [--rounds N] [--out-dir <dir>]');
    const baseDir = resolve(values['base-dir']);
    const rounds = Number(values.rounds);
    if (!Number.isInteger(rounds) || rounds < 1) throw new Error('--rounds must be a positive integer');

    const tmp = values['out-dir'] ? resolve(values['out-dir']) : mkdtempSync(join(tmpdir(), 'v8s-ab-'));
    mkdirSync(tmp, { recursive: true });
    console.log(`A/B workdir: ${tmp}`);
    const sides = {
        base: { root: baseDir, dist: join(tmp, 'dist-base'), bin: join(tmp, 'v8s-base') },
        head: { root: repoRoot, dist: join(tmp, 'dist-head'), bin: join(tmp, 'v8s-head') }
    };
    for (const s of [sides.head, sides.base]) {
        await must('yarn', ['workspace', '@v8s/ui', 'build-only'], s.root);
        cpSync(join(s.root, 'packages/ui/dist'), s.dist, { recursive: true });
        await must('go', ['build', '-o', s.bin, './cmd/v8s'], join(s.root, 'agent'));
    }

    const outs: Record<'base' | 'head', string[]> = { base: [], head: [] };
    for (let n = 1; n <= rounds; n++) {
        const order = n % 2 ? (['base', 'head'] as const) : (['head', 'base'] as const);
        for (const side of order) {
            const out = join(tmp, `${side}-${n}.json`);
            await must('node', [join(here, 'run.ts')], uiDir, {
                ...process.env,
                PERF_VARIANTS: 'desktop',
                PERF_NO_ASSERT: '1',
                V8S_AGENT_BIN: sides[side].bin,
                PERF_DIST: sides[side].dist,
                PERF_OUT: out
            });
            outs[side].push(out);
        }
    }

    return await exec('node', [join(here, 'compare.ts'), '--base', ...outs.base, '--head', ...outs.head], uiDir);
}

main().then(
    code => process.exit(code),
    err => {
        console.error(err);
        process.exit(2);
    }
);
