import { describe, expect, it } from 'vitest';

import { unifiedToSides } from '../src/lib/unified.ts';

describe('unifiedToSides', () => {
    it('returns empty sides for an empty diff', () => {
        expect(unifiedToSides('')).toEqual({ left: '', right: '' });
        expect(unifiedToSides('  \n')).toEqual({ left: '', right: '' });
    });

    it('splits context, deletions and additions and keeps hunk headers on both sides', () => {
        const d = ['--- a', '+++ b', '@@ -1,3 +1,3 @@', ' a', '-b', '+B', ' c', '@@ -10,2 +10,2 @@', ' x', '-y', '+z', ''].join('\n');
        const { left, right } = unifiedToSides(d);
        expect(left).toBe('@@ -1,3 +1,3 @@\na\nb\nc\n@@ -10,2 +10,2 @@\nx\ny');
        expect(right).toBe('@@ -1,3 +1,3 @@\na\nB\nc\n@@ -10,2 +10,2 @@\nx\nz');
    });

    it('ignores the no-newline marker and drops file headers', () => {
        const { left, right } = unifiedToSides('--- a\n+++ b\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n');
        expect(left).toBe('@@ -1 +1 @@\na');
        expect(right).toBe('@@ -1 +1 @@\nb');
    });
});
