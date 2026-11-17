/** Pure extraction of Flux HelmRelease remediation state from a parsed object. */

export interface RemCondition {
    type: string;
    status: string;
    reason: string;
    message: string;
}

export interface RemStrategy {
    /** `spec.<phase>.remediation.retries`; `null` when unset (Flux default: 0), -1 = unlimited. */
    retries: number | null;
    strategy: string;
}

export interface HelmHistoryEntry {
    digest: string;
    version: number | null;
    status: string;
    chartName: string;
    chartVersion: string;
    appVersion: string;
    firstDeployed: string;
    lastDeployed: string;
}

export interface Remediation {
    conditions: RemCondition[];
    failures: number;
    installFailures: number;
    upgradeFailures: number;
    install: RemStrategy;
    upgrade: RemStrategy;
    lastAttemptedRevision: string;
    lastAppliedRevision: string;
    helmChart: string;
    history: HelmHistoryEntry[];
    lastHandledReconcileAt: string;
    requestedAt: string;
    /** A reconcile was requested and the controller has not handled it yet. */
    reconcilePending: boolean;
    suspended: boolean;
    releaseName: string;
    releaseNamespace: string;
}

const CONDITION_ORDER = ['Ready', 'Stalled', 'Remediated', 'Released'];

type Obj = Record<string, unknown>;
const obj = (v: unknown): Obj => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Obj) : {});
const str = (v: unknown): string => (typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '');
const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

function strategyOf(spec: Obj, phase: 'install' | 'upgrade'): RemStrategy {
    const rem = obj(obj(spec[phase]).remediation);
    const retries = typeof rem.retries === 'number' ? rem.retries : null;
    return { retries, strategy: str(rem.strategy) || (phase === 'upgrade' ? 'rollback' : 'uninstall') };
}

/** `-1` -> "unlimited", unset -> "0 (default)". */
export function retriesLabel(retries: number | null): string {
    if (retries === null) return '0 (default)';
    return retries < 0 ? 'unlimited' : String(retries);
}

/** True when failures have used up the configured retries (retries are on top of the first attempt). */
export function retriesExhausted(failures: number, retries: number | null): boolean {
    if (retries !== null && retries < 0) return false;
    return failures > (retries ?? 0);
}

export function remediationOf(parsed: Record<string, unknown> | null): Remediation {
    const meta = obj(parsed?.metadata);
    const spec = obj(parsed?.spec);
    const status = obj(parsed?.status);
    const ann = obj(meta.annotations);

    const conds = Array.isArray(status.conditions) ? status.conditions.map(obj) : [];
    const conditions: RemCondition[] = [];
    for (const type of CONDITION_ORDER) {
        const c = conds.find(x => x.type === type);
        if (c) conditions.push({ type, status: str(c.status), reason: str(c.reason), message: str(c.message) });
    }

    const history: HelmHistoryEntry[] = (Array.isArray(status.history) ? status.history : []).map(h => {
        const o = obj(h);
        return {
            digest: str(o.digest),
            version: typeof o.version === 'number' ? o.version : null,
            status: str(o.status),
            chartName: str(o.chartName),
            chartVersion: str(o.chartVersion),
            appVersion: str(o.appVersion),
            firstDeployed: str(o.firstDeployed),
            lastDeployed: str(o.lastDeployed)
        };
    });

    const lastHandled = str(status.lastHandledReconcileAt);
    const requestedAt = str(ann['reconcile.fluxcd.io/requestedAt']);
    const targetNs = str(spec.targetNamespace);
    const firstHist = obj(Array.isArray(status.history) ? status.history[0] : null);
    const metaName = str(meta.name);

    return {
        conditions,
        failures: num(status.failures),
        installFailures: num(status.installFailures),
        upgradeFailures: num(status.upgradeFailures),
        install: strategyOf(spec, 'install'),
        upgrade: strategyOf(spec, 'upgrade'),
        lastAttemptedRevision: str(status.lastAttemptedRevision),
        lastAppliedRevision: str(status.lastAppliedRevision),
        helmChart: str(status.helmChart),
        history,
        lastHandledReconcileAt: lastHandled,
        requestedAt,
        reconcilePending: !!requestedAt && requestedAt !== lastHandled,
        suspended: spec.suspend === true,
        // Flux's own naming: spec.releaseName, else "<targetNamespace>-<name>" when targetNamespace is set, else the object name.
        // The latest history entry (what Helm actually stored) wins over the derived default.
        releaseName: str(spec.releaseName) || str(firstHist.name) || (targetNs ? `${targetNs}-${metaName}` : metaName),
        releaseNamespace: str(firstHist.namespace) || targetNs || str(meta.namespace)
    };
}
