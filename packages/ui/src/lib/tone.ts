/** ColTone -> `.tag` modifier class. */
export function toneClass(tone: number): string {
    return ['', 'success', 'warning', 'danger', 'info', 'release'][tone] ?? '';
}
