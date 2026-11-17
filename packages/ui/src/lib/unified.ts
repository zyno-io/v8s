/**
 * Splits a unified diff into the two texts it compares, for the side-by-side DiffView. Context and deleted lines go left,
 * context and added lines go right; every `@@ … @@` hunk header is kept on both sides as a separator so the LCS alignment
 * in DiffView stays anchored at the hunk boundaries. File headers (`---`, `+++`, `diff`, `index`) before a hunk are dropped.
 */
export function unifiedToSides(diff: string): { left: string; right: string } {
    if (!diff.trim()) return { left: '', right: '' };
    const left: string[] = [];
    const right: string[] = [];
    let inHunk = false;
    for (const line of diff.split('\n')) {
        if (line.startsWith('@@')) {
            inHunk = true;
            left.push(line);
            right.push(line);
            continue;
        }
        if (!inHunk) continue;
        const c = line[0];
        if (c === '-') left.push(line.slice(1));
        else if (c === '+') right.push(line.slice(1));
        else if (c === '\\') continue; // "\ No newline at end of file"
        else if (c === ' ') {
            left.push(line.slice(1));
            right.push(line.slice(1));
        } else if (line === '') {
            // A trailing newline, or an empty context line whose leading space was stripped.
            continue;
        } else {
            left.push(line);
            right.push(line);
        }
    }
    return { left: left.join('\n'), right: right.join('\n') };
}
