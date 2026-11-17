// Bundles the Electron main and preload scripts. Preload must be a single CommonJS file because it runs sandboxed.
import { build } from 'esbuild';

const common = { bundle: true, platform: 'node', target: 'node22', external: ['electron'], format: 'cjs', sourcemap: true, logLevel: 'info' };

await build({ ...common, entryPoints: ['src/main.ts'], outfile: 'out/main.cjs' });
await build({ ...common, entryPoints: ['src/preload.ts'], outfile: 'out/preload.cjs' });
