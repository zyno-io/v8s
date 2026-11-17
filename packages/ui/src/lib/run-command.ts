import type { Router } from 'vue-router';

import { useClustersStore } from '@/stores/clusters.ts';
import { usePodsStore } from '@/stores/pods.ts';
import { useUiStore } from '@/stores/ui.ts';

import type { Command } from './command-parser.ts';

/** Applies a parsed command. Returns an error message to show inline, or null when the bar should close. */
export async function runCommand(cmd: Command, router: Router): Promise<string | null> {
    const clusters = useClustersStore();
    const pods = usePodsStore();
    const ui = useUiStore();
    switch (cmd.type) {
        case 'empty':
        case 'quit':
            return null;
        case 'error':
            return cmd.message;
        case 'ctx-open':
            ui.commandBarOpen = false;
            clusters.open();
            return null;
        case 'ctx-set':
            void clusters.connect(cmd.ids);
            return null;
        case 'ctx-delta': {
            const next = new Set(clusters.activeIds);
            for (const id of cmd.remove) next.delete(id);
            for (const id of cmd.add) next.add(id);
            void clusters.connect([...next]);
            return null;
        }
        case 'ns':
            pods.namespaces = cmd.namespace ? [cmd.namespace] : [];
            return null;
        case 'problems':
            await router.push('/problems');
            return null;
        case 'open-kind':
            // Only pods has a view in P0; the parser already rejects kinds with available:false.
            await router.push('/pods');
            if (cmd.namespace) pods.namespaces = [cmd.namespace];
            if (cmd.name) {
                pods.text = cmd.name;
                pods.pendingOpen = { namespace: cmd.namespace ?? '', name: cmd.name };
            }
            return null;
    }
}
