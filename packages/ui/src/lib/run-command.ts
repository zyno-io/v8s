import type { Router } from 'vue-router';

import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

import type { Command } from './command-parser.ts';

import { kinds } from './agent.ts';
import { kindLabel, labelFromGvr } from './kinds.ts';
import { errorText } from './mutations.ts';
import { recordRecentNamespaces } from './recent-namespaces.ts';
import { gvrFromRoute, gvrPath } from './route.ts';

/** Applies a parsed command. Returns an error message to show inline, or null when the bar should close. */
export async function runCommand(cmd: Command, router: Router): Promise<string | null> {
    const clusters = useClustersStore();
    const table = useTableStore();
    const settings = useSettingsStore();
    const ui = useUiStore();
    const route = router.currentRoute.value;
    const here = gvrFromRoute(route.path, route.params);
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
            if (!here) return 'open a resource list first';
            table.of(here).namespaces = cmd.namespaces ?? [];
            // Naming namespaces makes them the most recent set (keys 1–9); `all` / `*` does not.
            if (cmd.namespaces) recordRecentNamespaces(cmd.namespaces);
            return null;
        case 'ns-delta': {
            if (!here) return 'open a resource list first';
            const st = table.of(here);
            const next = st.namespaces.filter(n => !cmd.remove.includes(n));
            for (const n of cmd.add) if (!next.includes(n)) next.push(n);
            st.namespaces = next;
            recordRecentNamespaces(next);
            return null;
        }
        case 'problems':
            await router.push('/problems');
            return null;
        case 'star': {
            if (!here) return 'open a resource type first';
            if (settings.isStarred(here) === cmd.on) return null;
            const label = kinds.value.find(k => k.gvr === here);
            try {
                await settings.toggleStarred(here);
                ui.toast('success', `${cmd.on ? 'Starred' : 'Unstarred'} ${label ? kindLabel(label) : labelFromGvr(here)}`);
            } catch (e) {
                return errorText(e);
            }
            return null;
        }
        case 'ip':
            if (!useClustersStore().anyConnected) return 'connect a cluster first';
            ui.commandBarOpen = false;
            ui.paletteQuery = cmd.text;
            ui.paletteOpen = true;
            return null;
        case 'open-kind': {
            await router.push(gvrPath(cmd.gvr));
            const st = table.of(cmd.gvr);
            if (cmd.text) {
                st.namespaces = [];
                st.text = cmd.text;
            }
            if (cmd.namespaces) st.namespaces = [...cmd.namespaces];
            else if (cmd.namespace) st.namespaces = [cmd.namespace];
            if (cmd.namespaces?.length) recordRecentNamespaces(cmd.namespaces);
            else if (cmd.namespace) recordRecentNamespaces([cmd.namespace]);
            if (cmd.name) {
                st.text = cmd.name;
                st.pendingOpen = { namespace: cmd.namespace ?? '', name: cmd.name };
            }
            return null;
        }
    }
}
