import type { ProblemsClassesResult, ViewHandle, ViewQuery } from '@v8s/client';

import { ProblemsGVR } from '@v8s/client';
import { computed, onScopeDispose, shallowRef, watch } from 'vue';

import type { GroupSpec } from '@/lib/problems-groups.ts';
import type { GroupBy, ProblemsFilters } from '@/lib/problems.ts';

import { getAgent, getNamespacesView, getProblemsAll } from '@/lib/agent.ts';
import { namespaceNames } from '@/lib/namespaces.ts';
import {
    AGGREGATE_CAP,
    buildProblemsQuery,
    classesWithProblems,
    groupCountQuery,
    makeRefresher,
    MAX_GROUP_KEYS,
    problemCols,
    problemTotals,
    sortGroupKeys
} from '@/lib/problems.ts';
import { windowPad } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useProblemsStore } from '@/stores/problems.ts';
import { useTableStore } from '@/stores/table.ts';

/**
 * The Problems page data layer: the one list view (sorted for grouping or aggregation), a one-row count view per group key,
 * and `problems.classes` (chip counts, refreshed at most once a second while the view changes).
 */
export function useProblemsView() {
    const agent = getAgent();
    const store = useProblemsStore();
    const table = useTableStore();
    const clusters = useClustersStore();
    const nsState = table.of(ProblemsGVR);

    // ---- classes (chip counts, switches, unavailable checks) ----
    const classes = shallowRef<ProblemsClassesResult | null>(null);
    const classesError = shallowRef('');
    let disposed = false;
    async function loadClasses() {
        try {
            const r = await agent.call<ProblemsClassesResult>('problems.classes');
            if (!disposed) {
                classes.value = r;
                classesError.value = '';
            }
        } catch (e) {
            if (!disposed) classesError.value = e instanceof Error ? e.message : String(e);
        }
    }
    const refresher = makeRefresher(() => void loadClasses());
    void loadClasses();

    // ---- the list ----
    const filters = computed<ProblemsFilters>(() => ({
        families: store.families,
        severity: store.severity,
        showAcked: store.showAcked,
        text: store.text,
        namespaces: nsState.namespaces
    }));

    const namespaceView = getNamespacesView();
    const namespaceKeys = computed(() => {
        void namespaceView.version.value;
        return nsState.namespaces.length ? [...nsState.namespaces] : namespaceNames(namespaceView);
    });
    const namespaceTooMany = computed(() => namespaceKeys.value.length > MAX_GROUP_KEYS);

    const initialLimit = () => windowPad() * 2 + 60;
    // The total does not depend on the sort, so deciding aggregation from it cannot flip-flop.
    const aggregateActive = computed(() => store.aggregate && main.total.value <= AGGREGATE_CAP);
    const aggregateBlocked = computed(() => store.aggregate && main.total.value > AGGREGATE_CAP);
    const group = computed<GroupBy>(() =>
        aggregateActive.value ? 'none' : store.groupBy === 'namespace' && namespaceTooMany.value ? 'none' : store.groupBy
    );
    const query = computed<ViewQuery>(() =>
        buildProblemsQuery(filters.value, { group: group.value, oldest: store.oldest, aggregate: aggregateActive.value })
    );

    const main: ViewHandle = agent.subscribeView(
        buildProblemsQuery(filters.value, { group: store.groupBy === 'namespace' ? 'none' : store.groupBy, oldest: store.oldest, aggregate: false }),
        { offset: 0, limit: initialLimit() }
    );
    const cols = computed(() => problemCols(main.columns.value));

    const windowFor = () => ({ offset: 0, limit: aggregateActive.value ? Math.max(1, main.total.value) : initialLimit() });
    watch(
        query,
        q => {
            if (JSON.stringify(q) !== JSON.stringify(main.query)) main.update({ query: q, window: windowFor() });
        },
        { immediate: true }
    );
    // Aggregation needs the whole set in one window.
    watch(
        () => [aggregateActive.value, main.total.value] as const,
        ([on, total]) => {
            if (on && (main.window.limit < total || main.window.offset !== 0)) main.update({ window: windowFor() });
        }
    );

    // ---- group sizes ----
    const handles = new Map<string, ViewHandle>();
    const tick = shallowRef(0);
    const activeIds = computed(() => clusters.activeIds);
    const groupKeys = computed<string[]>(() => {
        switch (group.value) {
            case 'class':
                return classesWithProblems(classes.value?.classes ?? [], activeIds.value, store.families);
            case 'cluster':
                return [...activeIds.value];
            case 'namespace':
                return namespaceKeys.value.filter(Boolean);
            default:
                return [];
        }
    });
    function syncGroups() {
        const g = group.value;
        const keys = g === 'none' ? new Set<string>() : new Set(groupKeys.value.slice(0, MAX_GROUP_KEYS));
        let changed = false;
        for (const [k, h] of handles) {
            if (!keys.has(k)) {
                h.close();
                handles.delete(k);
                changed = true;
            }
        }
        if (g !== 'none') {
            for (const k of keys) {
                const q = groupCountQuery(filters.value, g, k);
                const h = handles.get(k);
                if (!h) {
                    handles.set(k, agent.subscribeView(q, { offset: 0, limit: 1 }));
                    changed = true;
                } else if (JSON.stringify(h.query) !== JSON.stringify(q)) {
                    h.update({ query: q });
                }
            }
        }
        if (changed) tick.value++;
    }
    watch([group, groupKeys, filters], syncGroups, { immediate: true });

    const groupSpecs = computed<GroupSpec[]>(() => {
        void tick.value;
        const g = group.value;
        if (g === 'none') return [{ key: '', count: main.total.value, collapsed: false }];
        const specs: GroupSpec[] = [];
        let sum = 0;
        for (const k of groupKeys.value.slice(0, MAX_GROUP_KEYS)) {
            const n = handles.get(k)?.total.value ?? 0;
            sum += n;
            specs.push({ key: k, count: n, collapsed: store.collapsed.has(`${g}:${k}`) });
        }
        // Cluster-scoped problems have an empty namespace: whatever the namespaces do not account for.
        if (g === 'namespace') specs.push({ key: '', count: Math.max(0, main.total.value - sum), collapsed: store.collapsed.has('namespace:') });
        const order = sortGroupKeys(specs.map(s => s.key));
        const by = new Map(specs.map(s => [s.key, s]));
        return order.map(k => by.get(k)!);
    });

    // ---- keep the counts fresh: at most once a second ----
    const all = getProblemsAll();
    watch(
        () => [main.version.value, main.total.value, all.total.value],
        () => refresher.schedule()
    );

    const totals = computed(() => problemTotals(classes.value?.classes ?? [], activeIds.value));

    const unavailable = computed(() => {
        const by = new Map<string, string[]>();
        for (const u of classes.value?.unavailable ?? []) {
            if (!activeIds.value.includes(u.cluster) || store.dismissed.has(`${u.cluster}|${u.kind}`)) continue;
            (by.get(u.cluster) ?? by.set(u.cluster, []).get(u.cluster)!).push(u.kind);
        }
        return [...by.entries()].map(([cluster, kinds]) => ({
            cluster,
            keys: kinds.map(k => `${cluster}|${k}`),
            text: `Some checks are unavailable in ${clusters.displayName(cluster)} (no permission to list ${kinds.join(', ')})`
        }));
    });

    onScopeDispose(() => {
        disposed = true;
        refresher.cancel();
        main.close();
        for (const h of handles.values()) h.close();
        handles.clear();
    });

    return {
        handle: main,
        query,
        cols,
        classes,
        classesError,
        refreshClasses: () => refresher.schedule(),
        reloadClasses: loadClasses,
        filters,
        group,
        groupSpecs,
        groupTick: tick,
        aggregateActive,
        aggregateBlocked,
        namespaceTooMany,
        totals,
        unavailable,
        initialLimit
    };
}

export type ProblemsView = ReturnType<typeof useProblemsView>;
