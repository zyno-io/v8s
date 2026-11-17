import { beforeEach, describe, expect, it, vi } from 'vitest';

const ask = vi.fn();
const guard = vi.fn();
const runFluxAction = vi.fn();

vi.mock('../src/lib/ask.ts', () => ({ ask: (...a: unknown[]) => ask(...a) }));
vi.mock('../src/lib/confirm-guard.ts', () => ({ confirmGuard: (...a: unknown[]) => guard(...a) }));
vi.mock('../src/lib/flux-actions.ts', () => ({ runFluxAction: (...a: unknown[]) => runFluxAction(...a) }));
vi.mock('../src/lib/mutations.ts', () => ({ runAction: vi.fn(), errorText: String }));
vi.mock('../src/lib/drawer.ts', () => ({ openObject: vi.fn() }));
vi.mock('../src/stores/ui.ts', () => ({ useUiStore: () => ({ toast: vi.fn() }) }));

import type { ProblemRow } from '../src/lib/problems.ts';

import { runProblemAction } from '../src/lib/problems-actions.ts';

const row = { cluster: 'c1', kind: 'Kustomization', namespace: 'flux-system', name: 'apps' } as ProblemRow;
const ctx = {
    router: {} as never,
    kinds: [],
    row,
    ev: { subject: { gvr: 'kustomize.toolkit.fluxcd.io/v1/kustomizations', namespace: 'flux-system', name: 'apps' } } as never
};

describe('problems reconcile quick action', () => {
    beforeEach(() => {
        ask.mockReset();
        guard.mockReset();
        runFluxAction.mockReset();
    });

    it('asks in a production group and does not run when declined', async () => {
        guard.mockReturnValue({ note: 'Production group: prod.' });
        ask.mockResolvedValue(false);
        await runProblemAction('reconcile', ctx);
        expect(ask).toHaveBeenCalledOnce();
        expect(ask.mock.calls[0]![0]).toMatchObject({ note: 'Production group: prod.' });
        expect(runFluxAction).not.toHaveBeenCalled();
    });

    it('runs after the production confirmation', async () => {
        guard.mockReturnValue({ note: 'Production group: prod.' });
        ask.mockResolvedValue(true);
        await runProblemAction('reconcile', ctx);
        expect(runFluxAction).toHaveBeenCalledWith([expect.objectContaining({ cluster: 'c1', name: 'apps' })], 'flux.reconcile');
    });

    it('runs at once outside production', async () => {
        guard.mockReturnValue({});
        await runProblemAction('reconcile', ctx);
        expect(ask).not.toHaveBeenCalled();
        expect(runFluxAction).toHaveBeenCalledOnce();
    });
});
