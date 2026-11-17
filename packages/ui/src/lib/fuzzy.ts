/** Score for matching `query` against `candidate`: 0-99 prefix (lower is better), 100+ fuzzy subsequence, -1 no match. */
export function matchScore(query: string, candidate: string): number {
    const q = query.toLowerCase();
    const c = candidate.toLowerCase();
    if (!q) return 0;
    if (c.startsWith(q)) return c.length - q.length;
    let ci = 0;
    let gaps = 0;
    let last = -1;
    for (let qi = 0; qi < q.length; qi++) {
        const found = c.indexOf(q[qi], ci);
        if (found < 0) return -1;
        if (last >= 0) gaps += found - last - 1;
        last = found;
        ci = found + 1;
    }
    return 100 + gaps + (c.length - q.length);
}

/** Prefix matches first, then fuzzy; stable for equal scores. */
export function rank<T>(query: string, items: readonly T[], text: (item: T) => string): T[] {
    const scored: { item: T; score: number; i: number }[] = [];
    items.forEach((item, i) => {
        const score = matchScore(query, text(item));
        if (score >= 0) scored.push({ item, score, i });
    });
    scored.sort((a, b) => a.score - b.score || a.i - b.i);
    return scored.map(s => s.item);
}
