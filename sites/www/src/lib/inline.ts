/** Escape HTML, then turn `backtick` spans into <code>. For trusted data strings only. */
export function inline(s: string): string {
  const esc = s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  return esc.replace(/`([^`]+)`/g, '<code>$1</code>');
}

/** Trim to a meta-description length without cutting mid-word. */
export function clip(s: string, max = 160): string {
  if (s.length <= max) return s;
  return s.slice(0, max - 1).replace(/\s+\S*$/, '') + '…';
}
