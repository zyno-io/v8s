import type { LinkCandidate, LinkSuggestion } from '@/lib/link-suggest.ts';

/** One settings patch that links every suggested pair, and the set of write contexts that must leave the selection. */
export function linkAllPatch(pairs: readonly Pick<LinkSuggestion, 'read' | 'write'>[]): {
    patch: { links: Record<string, string> };
    writes: Set<string>;
} {
    const links: Record<string, string> = {};
    for (const p of pairs) links[p.read] = p.write;
    return { patch: { links }, writes: new Set(pairs.map(p => p.write)) };
}

/** Contexts that can still take part in a new link: not linked yet (as read or write) and not the other side's pick. */
export function pairOptions(contexts: readonly LinkCandidate[], links: Readonly<Record<string, string>>, other = ''): LinkCandidate[] {
    const linked = new Set<string>([...Object.keys(links), ...Object.values(links)]);
    return contexts.filter(c => !linked.has(c.id) && c.id !== other);
}
