import { defineStore } from 'pinia';
import { ref, shallowRef } from 'vue';

import type { GroupBy, SeverityFilter } from '@/lib/problems.ts';

/** Problems page UI state (controls only; rows live in the agent and the view handle). */
export const useProblemsStore = defineStore('problems', () => {
    const severity = ref<SeverityFilter>('all');
    const groupBy = ref<GroupBy>('none');
    const showAcked = ref(false);
    const text = ref('');
    const families = ref<string[]>([]);
    const oldest = ref(false);
    const aggregate = ref(false);
    /** `<groupBy>:<key>` of collapsed group headers. */
    const collapsed = shallowRef<ReadonlySet<string>>(new Set());
    /** Unavailable-check notes the user dismissed (`cluster|kind`). */
    const dismissed = shallowRef<ReadonlySet<string>>(new Set());

    function toggleFamily(f: string) {
        families.value = families.value.includes(f) ? families.value.filter(x => x !== f) : [...families.value, f];
    }
    function toggleCollapsed(key: string) {
        const n = new Set(collapsed.value);
        if (!n.delete(key)) n.add(key);
        collapsed.value = n;
    }
    function dismiss(keys: string[]) {
        dismissed.value = new Set([...dismissed.value, ...keys]);
    }
    function clearFilters() {
        families.value = [];
        text.value = '';
        severity.value = 'all';
    }

    return {
        severity,
        groupBy,
        showAcked,
        text,
        families,
        oldest,
        aggregate,
        collapsed,
        dismissed,
        toggleFamily,
        toggleCollapsed,
        dismiss,
        clearFilters
    };
});
