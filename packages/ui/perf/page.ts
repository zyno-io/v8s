import type { Page } from 'playwright';

/** Opens the app and connects every cluster (the selector, or an already connected session), waiting for the first rows. */
export async function connectAll(page: Page, url: string) {
    await page.goto(url);
    const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
    const firstRow = page.locator('.scroller .row:not(.sk), .scroller .pcard:not(.sk)').first();
    // The agent keeps the cluster selection per process, so a later page load may already be connected.
    await Promise.race([dialog.waitFor({ timeout: 20000 }), firstRow.waitFor({ timeout: 20000 })]);
    if (await dialog.isVisible()) {
        const boxes = dialog.locator('.crow input[type=checkbox]');
        for (const box of await boxes.all()) if (!(await box.isChecked())) await box.check();
        await dialog.getByRole('button', { name: 'Connect' }).click();
    }
    await firstRow.waitFor({ timeout: 30000 });
}

/** Waits until the unfiltered total stops growing (full sync). */
export async function waitSynced(page: Page, expected: number): Promise<number> {
    const read = () => page.evaluate(() => Number(document.querySelector('.scroller')?.getAttribute('aria-rowcount') ?? 0) - 1);
    let last = -1;
    let stable = 0;
    const start = Date.now();
    while (Date.now() - start < 60000) {
        const n = await read();
        if (n >= expected || (n === last && n > 0 && ++stable >= 6)) return n;
        if (n !== last) stable = 0;
        last = n;
        await page.waitForTimeout(250);
    }
    return last;
}
