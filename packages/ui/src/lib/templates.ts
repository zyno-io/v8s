import type { Kind } from '@v8s/client';

export interface CreateTemplate {
    gvr: string;
    kind: string;
    label: string;
    namespaced: boolean;
    yaml: (ns: string) => string;
}

const meta = (name: string, ns: string, namespaced: boolean) => `metadata:\n  name: ${name}${namespaced ? `\n  namespace: ${ns || 'default'}` : ''}`;

export const TEMPLATES: CreateTemplate[] = [
    {
        gvr: 'v1/namespaces',
        kind: 'Namespace',
        label: 'Namespace',
        namespaced: false,
        yaml: () => `apiVersion: v1\nkind: Namespace\n${meta('my-namespace', '', false)}\n`
    },
    {
        gvr: 'v1/configmaps',
        kind: 'ConfigMap',
        label: 'ConfigMap',
        namespaced: true,
        yaml: ns => `apiVersion: v1\nkind: ConfigMap\n${meta('my-config', ns, true)}\ndata:\n  key: value\n`
    },
    {
        gvr: 'v1/secrets',
        kind: 'Secret',
        label: 'Secret',
        namespaced: true,
        yaml: ns => `apiVersion: v1\nkind: Secret\n${meta('my-secret', ns, true)}\ntype: Opaque\nstringData:\n  key: value\n`
    },
    {
        gvr: 'apps/v1/deployments',
        kind: 'Deployment',
        label: 'Deployment',
        namespaced: true,
        yaml: ns =>
            `apiVersion: apps/v1\nkind: Deployment\n${meta('my-app', ns, true)}\n  labels:\n    app: my-app\nspec:\n  replicas: 1\n  selector:\n    matchLabels:\n      app: my-app\n  template:\n    metadata:\n      labels:\n        app: my-app\n    spec:\n      containers:\n        - name: app\n          image: nginx:1.27\n          ports:\n            - containerPort: 80\n`
    },
    {
        gvr: 'v1/services',
        kind: 'Service',
        label: 'Service',
        namespaced: true,
        yaml: ns =>
            `apiVersion: v1\nkind: Service\n${meta('my-app', ns, true)}\nspec:\n  selector:\n    app: my-app\n  ports:\n    - port: 80\n      targetPort: 80\n`
    },
    {
        gvr: 'networking.k8s.io/v1/ingresses',
        kind: 'Ingress',
        label: 'Ingress',
        namespaced: true,
        yaml: ns =>
            `apiVersion: networking.k8s.io/v1\nkind: Ingress\n${meta('my-app', ns, true)}\nspec:\n  rules:\n    - host: app.example.com\n      http:\n        paths:\n          - path: /\n            pathType: Prefix\n            backend:\n              service:\n                name: my-app\n                port:\n                  number: 80\n`
    },
    {
        gvr: 'batch/v1/cronjobs',
        kind: 'CronJob',
        label: 'CronJob',
        namespaced: true,
        yaml: ns =>
            `apiVersion: batch/v1\nkind: CronJob\n${meta('my-cron', ns, true)}\nspec:\n  schedule: "*/5 * * * *"\n  jobTemplate:\n    spec:\n      template:\n        spec:\n          restartPolicy: OnFailure\n          containers:\n            - name: job\n              image: busybox:1.36\n              command: ["sh", "-c", "date"]\n`
    },
    {
        gvr: 'batch/v1/jobs',
        kind: 'Job',
        label: 'Job',
        namespaced: true,
        yaml: ns =>
            `apiVersion: batch/v1\nkind: Job\n${meta('my-job', ns, true)}\nspec:\n  backoffLimit: 2\n  template:\n    spec:\n      restartPolicy: Never\n      containers:\n        - name: job\n          image: busybox:1.36\n          command: ["sh", "-c", "echo done"]\n`
    }
];

export function templateFor(gvr: string): CreateTemplate | undefined {
    return TEMPLATES.find(t => t.gvr === gvr);
}

/** Resolves a gvr from the first document's `apiVersion`/`kind` using the discovered kinds. */
export function gvrFromManifest(doc: { apiVersion?: unknown; kind?: unknown } | null | undefined, kinds: readonly Kind[]): string | null {
    if (!doc || typeof doc.apiVersion !== 'string' || typeof doc.kind !== 'string') return null;
    const slash = doc.apiVersion.indexOf('/');
    const group = slash < 0 ? '' : doc.apiVersion.slice(0, slash);
    const version = slash < 0 ? doc.apiVersion : doc.apiVersion.slice(slash + 1);
    const exact = kinds.find(k => k.kind === doc.kind && k.group === group && k.version === version);
    if (exact) return exact.gvr;
    const loose = kinds.find(k => k.kind === doc.kind && k.group === group);
    return loose?.gvr ?? null;
}
