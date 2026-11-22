/**
 * Theme, as the v8s app's own System / Light / Dark setting.
 *
 * "system" follows prefers-color-scheme; "light" / "dark" pin it with
 * <html data-theme>, kept in sessionStorage for the tab. Two controls drive
 * it: the header's toggle (flips what you see now) and the radio group in the
 * [ui] section (`input[name="ui-theme"]`). Both stay in sync.
 */
type Choice = 'system' | 'light' | 'dark';

const root = document.documentElement;
const mq = window.matchMedia('(prefers-color-scheme: dark)');
const KEY = 'v8s-theme';

function choice(): Choice {
  const t = root.getAttribute('data-theme');
  return t === 'light' || t === 'dark' ? t : 'system';
}
function isDark(): boolean {
  const c = choice();
  return c === 'system' ? mq.matches : c === 'dark';
}

function apply(next: Choice) {
  if (next === 'system') root.removeAttribute('data-theme');
  else root.setAttribute('data-theme', next);
  try {
    if (next === 'system') sessionStorage.removeItem(KEY);
    else sessionStorage.setItem(KEY, next);
  } catch {
    /* storage blocked: the choice still holds for this page view */
  }
  sync();
}

function sync() {
  const dark = isDark();
  document.querySelectorAll<HTMLButtonElement>('[data-theme-toggle]').forEach((btn) => {
    btn.setAttribute('aria-label', dark ? 'Switch to light theme' : 'Switch to dark theme');
  });
  const c = choice();
  document.querySelectorAll<HTMLInputElement>('input[name="ui-theme"]').forEach((r) => {
    r.checked = r.value === c;
  });
  document.querySelectorAll<HTMLElement>('[data-theme-resolved]').forEach((el) => {
    el.textContent = dark ? 'dark' : 'light';
  });
}

document.querySelectorAll<HTMLButtonElement>('[data-theme-toggle]').forEach((btn) =>
  btn.addEventListener('click', () => apply(isDark() ? 'light' : 'dark')),
);
document.querySelectorAll<HTMLInputElement>('input[name="ui-theme"]').forEach((r) =>
  r.addEventListener('change', () => r.checked && apply(r.value as Choice)),
);
mq.addEventListener('change', sync);
sync();
