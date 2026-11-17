// Node/V8 decode + retention + worker-transfer benchmark.
// usage: node --expose-gc node-bench.mjs out/pods-10k.json
import { readFileSync } from 'node:fs';
import { Worker } from 'node:worker_threads';

const file = process.argv[2] ?? 'out/pods-10k.json';
const raw = readFileSync(file, 'utf8');
const gc = globalThis.gc;
const heap = () => (gc(), gc(), process.memoryUsage().heapUsed);
const mb = (b) => (b / 1048576).toFixed(1) + ' MB';
const time = (label, fn, runs = 5) => {
    const ts = [];
    let r;
    for (let i = 0; i < runs; i++) {
        const t = performance.now();
        r = fn();
        ts.push(performance.now() - t);
    }
    ts.sort((a, b) => a - b);
    console.log(`${label.padEnd(52)} median ${ts[runs >> 1].toFixed(1)} ms`);
    return r;
};

console.log(`input: ${mb(Buffer.byteLength(raw))} JSON`);

// 1. Full JSON.parse of the list (what a naive renderer does on every list).
time('JSON.parse full list', () => JSON.parse(raw));

// 2. Retained heap: full objects vs stripped vs row tuples.
let base = heap();
let full = JSON.parse(raw).items;
const fullHeap = heap() - base;
console.log(`retained heap, full objects                          ${mb(fullHeap)}`);

const strip = (o) => {
    delete o.metadata.managedFields;
    if (o.metadata.annotations) delete o.metadata.annotations['kubectl.kubernetes.io/last-applied-configuration'];
    return o;
};
const strippedJson = time('strip managedFields + last-applied (in place)', () => JSON.stringify(JSON.parse(raw).items.map(strip)), 3);
console.log(`stripped list JSON size                              ${mb(Buffer.byteLength(strippedJson))}`);
full = null;
base = heap();
let stripped = JSON.parse(strippedJson);
console.log(`retained heap, stripped objects                      ${mb(heap() - base)}`);

const project = (p) => {
    const cs = p.status.containerStatuses ?? [];
    let restarts = 0, ready = 0;
    for (const c of cs) { restarts += c.restartCount; if (c.ready) ready++; }
    return [p.metadata.namespace, p.metadata.name, p.status.phase, ready, cs.length, restarts, p.spec.nodeName, Date.parse(p.metadata.creationTimestamp), p.metadata.uid];
};
const rows = time('project to row tuples (9 cols)', () => stripped.map(project));
stripped = null;
base = heap();
const rowsKeep = JSON.parse(JSON.stringify(rows));
console.log(`retained heap, row tuples                            ${mb(heap() - base)}`);

// 3. Columnar encoding: string table + typed arrays (transferable).
function columnar(rs) {
    const strings = new Map();
    const sid = (s) => { let id = strings.get(s); if (id === undefined) { id = strings.size; strings.set(s, id); } return id; };
    const n = rs.length;
    const ns = new Uint32Array(n), name = new Uint32Array(n), phase = new Uint32Array(n), node = new Uint32Array(n), uid = new Uint32Array(n);
    const ready = new Uint16Array(n), total = new Uint16Array(n), restarts = new Uint32Array(n), created = new Float64Array(n);
    for (let i = 0; i < n; i++) {
        const r = rs[i];
        ns[i] = sid(r[0]); name[i] = sid(r[1]); phase[i] = sid(r[2]); ready[i] = r[3]; total[i] = r[4]; restarts[i] = r[5]; node[i] = sid(r[6]); created[i] = r[7]; uid[i] = sid(r[8]);
    }
    const table = [...strings.keys()];
    return { table, cols: { ns, name, phase, ready, total, restarts, node, created, uid } };
}
const col = time('encode columnar (string table + typed arrays)', () => columnar(rowsKeep));

// 4. Worker transfer cost: structured clone of objects vs rows vs columnar (transfer).
const worker = new Worker(`const { parentPort } = require('node:worker_threads'); parentPort.on('message', (m) => parentPort.postMessage(1));`, { eval: true });
const roundtrip = (msg, transfer) => new Promise((res) => { const t = performance.now(); worker.once('message', () => res(performance.now() - t)); worker.postMessage(msg, transfer); });
const fullObjs = JSON.parse(strippedJson);
const bench = async (label, mk) => {
    const ts = [];
    for (let i = 0; i < 5; i++) { const [m, tr] = mk(); ts.push(await roundtrip(m, tr)); }
    ts.sort((a, b) => a - b);
    console.log(`${label.padEnd(52)} median ${ts[2].toFixed(1)} ms`);
};
await bench(`postMessage clone: ${rowsKeep.length} stripped full objects`, () => [fullObjs]);
await bench(`postMessage clone: ${rowsKeep.length} row tuples`, () => [rowsKeep]);
await bench('postMessage clone: raw JSON string (stripped)', () => [strippedJson]);
await bench('postMessage transfer: columnar typed arrays', () => {
    const c = columnar(rowsKeep);
    return [c, Object.values(c.cols).map((a) => a.buffer)];
});

// 5. Delta apply: 1% of rows change, update index map vs rebuild sort.
const idx = new Map(rowsKeep.map((r, i) => [r[8], i]));
time('apply 100 deltas via uid index (Map lookup+replace)', () => { for (let i = 0; i < 100; i++) { const r = rowsKeep[(i * 97) % rowsKeep.length]; rowsKeep[idx.get(r[8])] = r.slice(); } }, 5);
time(`full re-sort ${rowsKeep.length} rows by name (localeCompare)`, () => rowsKeep.slice().sort((a, b) => a[1].localeCompare(b[1])), 5);
const coll = new Intl.Collator('en');
time(`full re-sort ${rowsKeep.length} rows by name (Intl.Collator)`, () => rowsKeep.slice().sort((a, b) => coll.compare(a[1], b[1])), 5);
time(`full re-sort ${rowsKeep.length} rows by name (plain < compare)`, () => rowsKeep.slice().sort((a, b) => (a[1] < b[1] ? -1 : a[1] > b[1] ? 1 : 0)), 5);
time(`substring filter ${rowsKeep.length} rows ("svc-12")`, () => rowsKeep.filter((r) => r[1].includes('svc-12')), 5);
void col;
await worker.terminate();
