// Applies a stored manual theme before first paint. External file so the Electron CSP can stay `script-src 'self'`.
try {
    const m = localStorage.getItem('v8s:theme');
    if (m === 'light' || m === 'dark') document.documentElement.classList.add(m);
} catch {
    // storage unavailable: follow the OS theme
}
