// Generates a synthetic but realistically-shaped PodList (JSON) for decode benchmarks.
// Shape mirrors a typical Deployment-managed pod as returned by the API server, including
// managedFields and the kubectl last-applied annotation, which dominate size on real clusters.
//
// usage: node gen.mjs <count> <out.json>
import { openSync, writeSync, closeSync } from 'node:fs';

const count = Number(process.argv[2] ?? 10000);
const out = process.argv[3] ?? 'out/pods-10k.json';

const rnd = (n) => Math.floor(Math.random() * n);
const hex = (n) => Array.from({ length: n }, () => '0123456789abcdef'[rnd(16)]).join('');
const ts = () => new Date(Date.now() - rnd(30 * 86400e3)).toISOString().replace(/\.\d+Z$/, 'Z');

function pod(i) {
    const app = `svc-${i % 400}`;
    const ns = `team-${i % 37}`;
    const name = `${app}-${hex(10)}-${hex(5)}`;
    const containers = Array.from({ length: 1 + (i % 3) }, (_, c) => ({
        name: c === 0 ? app : `sidecar-${c}`,
        image: `registry.example.com/${ns}/${app}:${hex(12)}`,
        ports: [{ name: 'http', containerPort: 8080, protocol: 'TCP' }],
        env: Array.from({ length: 12 }, (_, e) => ({ name: `ENV_VAR_${e}`, value: `value-${hex(16)}` })),
        resources: { limits: { cpu: '500m', memory: '512Mi' }, requests: { cpu: '100m', memory: '128Mi' } },
        volumeMounts: [{ name: 'kube-api-access', readOnly: true, mountPath: '/var/run/secrets/kubernetes.io/serviceaccount' }],
        livenessProbe: { httpGet: { path: '/healthz', port: 8080, scheme: 'HTTP' }, periodSeconds: 10, timeoutSeconds: 1, successThreshold: 1, failureThreshold: 3 },
        readinessProbe: { httpGet: { path: '/ready', port: 8080, scheme: 'HTTP' }, periodSeconds: 5, timeoutSeconds: 1, successThreshold: 1, failureThreshold: 3 },
        terminationMessagePath: '/dev/termination-log',
        terminationMessagePolicy: 'File',
        imagePullPolicy: 'IfNotPresent',
    }));
    const lastApplied = JSON.stringify({ apiVersion: 'v1', kind: 'Pod', metadata: { name, namespace: ns, labels: { app } }, spec: { containers } });
    return {
        metadata: {
            name,
            generateName: `${app}-${hex(10)}-`,
            namespace: ns,
            uid: `${hex(8)}-${hex(4)}-${hex(4)}-${hex(4)}-${hex(12)}`,
            resourceVersion: String(1e8 + i),
            creationTimestamp: ts(),
            labels: { app, 'pod-template-hash': hex(10), 'app.kubernetes.io/part-of': ns },
            annotations: { 'kubectl.kubernetes.io/last-applied-configuration': lastApplied, 'prometheus.io/scrape': 'true' },
            ownerReferences: [{ apiVersion: 'apps/v1', kind: 'ReplicaSet', name: `${app}-${hex(10)}`, uid: hex(32), controller: true, blockOwnerDeletion: true }],
            managedFields: [
                { manager: 'kube-controller-manager', operation: 'Update', apiVersion: 'v1', time: ts(), fieldsType: 'FieldsV1', fieldsV1: { 'f:metadata': { 'f:generateName': {}, 'f:labels': { '.': {}, 'f:app': {}, 'f:pod-template-hash': {} }, 'f:ownerReferences': { '.': {}, [`k:{"uid":"${hex(32)}"}`]: {} } }, 'f:spec': Object.fromEntries(containers.map((c) => [`k:{"name":"${c.name}"}`, { '.': {}, 'f:env': Object.fromEntries(c.env.map((e) => [`k:{"name":"${e.name}"}`, { '.': {}, 'f:name': {}, 'f:value': {} }])), 'f:image': {}, 'f:imagePullPolicy': {}, 'f:livenessProbe': {}, 'f:readinessProbe': {}, 'f:resources': {} }])) } },
                { manager: 'kubelet', operation: 'Update', apiVersion: 'v1', time: ts(), fieldsType: 'FieldsV1', subresource: 'status', fieldsV1: { 'f:status': { 'f:conditions': {}, 'f:containerStatuses': {}, 'f:hostIP': {}, 'f:phase': {}, 'f:podIP': {}, 'f:podIPs': {}, 'f:startTime': {} } } },
            ],
        },
        spec: {
            volumes: [{ name: 'kube-api-access', projected: { sources: [{ serviceAccountToken: { expirationSeconds: 3607, path: 'token' } }, { configMap: { name: 'kube-root-ca.crt', items: [{ key: 'ca.crt', path: 'ca.crt' }] } }], defaultMode: 420 } }],
            containers,
            restartPolicy: 'Always',
            terminationGracePeriodSeconds: 30,
            dnsPolicy: 'ClusterFirst',
            serviceAccountName: 'default',
            serviceAccount: 'default',
            nodeName: `node-${i % 150}`,
            securityContext: {},
            schedulerName: 'default-scheduler',
            tolerations: [
                { key: 'node.kubernetes.io/not-ready', operator: 'Exists', effect: 'NoExecute', tolerationSeconds: 300 },
                { key: 'node.kubernetes.io/unreachable', operator: 'Exists', effect: 'NoExecute', tolerationSeconds: 300 },
            ],
            priority: 0,
            enableServiceLinks: true,
            preemptionPolicy: 'PreemptLowerPriority',
        },
        status: {
            phase: i % 97 === 0 ? 'Pending' : 'Running',
            conditions: ['Initialized', 'Ready', 'ContainersReady', 'PodScheduled'].map((type) => ({ type, status: 'True', lastProbeTime: null, lastTransitionTime: ts() })),
            hostIP: `10.0.${i % 250}.${i % 200}`,
            podIP: `10.244.${i % 250}.${i % 200}`,
            podIPs: [{ ip: `10.244.${i % 250}.${i % 200}` }],
            startTime: ts(),
            containerStatuses: containers.map((c) => ({
                name: c.name,
                state: { running: { startedAt: ts() } },
                lastState: {},
                ready: true,
                restartCount: i % 53 === 0 ? 14 : 0,
                image: c.image,
                imageID: `${c.image}@sha256:${hex(64)}`,
                containerID: `containerd://${hex(64)}`,
                started: true,
            })),
            qosClass: 'Burstable',
        },
    };
}

// Streamed so large lists don't hit V8's max string length (~512 MB).
const fd = openSync(out, 'w');
writeSync(fd, '{"kind":"PodList","apiVersion":"v1","metadata":{"resourceVersion":"123456789"},"items":[');
for (let i = 0; i < count; i++) writeSync(fd, (i ? ',' : '') + JSON.stringify(pod(i)));
writeSync(fd, ']}');
closeSync(fd);
console.log(`wrote ${count} pods to ${out}`);
