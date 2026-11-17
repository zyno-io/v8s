export interface LinkCandidate {
    id: string;
    name: string;
}

export interface LinkSuggestion {
    read: string;
    write: string;
    base: string;
}

const READ_ROLES = ['operator', 'read', 'readonly', 'read-only', 'ro', 'reader', 'viewer', 'view'];
const WRITE_ROLES = ['cluster-admin', 'admin', 'write', 'writer', 'rw', 'edit', 'editor', 'elevated', 'sudo', 'root', 'superuser'];

/** Splits `prod-us-cluster-admin` into base `prod-us` and role `cluster-admin`; role is '' when there is no known suffix. */
export function splitRole(name: string): { base: string; role: string; kind: 'read' | 'write' | 'none' } {
    const lower = name.toLowerCase();
    const roles = [...READ_ROLES.map(r => [r, 'read'] as const), ...WRITE_ROLES.map(r => [r, 'write'] as const)].sort(
        (a, b) => b[0].length - a[0].length
    );
    for (const [role, kind] of roles) {
        for (const sep of ['-', '_', '.', '@']) {
            const suffix = sep + role;
            if (lower.endsWith(suffix) && lower.length > suffix.length) return { base: name.slice(0, name.length - suffix.length), role, kind };
        }
    }
    return { base: name, role: '', kind: 'none' };
}

/**
 * Suggests read/write pairs among contexts whose names differ only by a role suffix (`edge-operator` / `edge-cluster-admin`,
 * or `prod-us` / `prod-us-admin`). A pair needs exactly one write-role member and exactly one read-ish member (a read-role
 * suffix or the bare base name). Already-linked and dismissed pairs are skipped, and contexts that already take part in a
 * link are not offered again.
 */
export function suggestLinks(
    contexts: readonly LinkCandidate[],
    links: Readonly<Record<string, string>>,
    dismissed: ReadonlySet<string> = new Set()
): LinkSuggestion[] {
    const linked = new Set<string>([...Object.keys(links), ...Object.values(links)]);
    const byBase = new Map<string, { read: LinkCandidate[]; write: LinkCandidate[] }>();
    for (const c of contexts) {
        if (linked.has(c.id)) continue;
        const { base, kind } = splitRole(c.name);
        const key = base.toLowerCase();
        const g = byBase.get(key) ?? { read: [], write: [] };
        if (kind === 'write') g.write.push(c);
        else g.read.push(c); // a read role or the bare name
        byBase.set(key, g);
    }
    const out: LinkSuggestion[] = [];
    for (const [, g] of byBase) {
        if (g.read.length !== 1 || g.write.length !== 1) continue;
        const read = g.read[0];
        const write = g.write[0];
        if (dismissed.has(`${read.id}|${write.id}`)) continue;
        out.push({ read: read.id, write: write.id, base: splitRole(write.name).base });
    }
    return out.sort((a, b) => a.read.localeCompare(b.read));
}
