<script setup lang="ts">
import { computed } from 'vue';

import type { ContainerRequests, Recommendation, Res } from '@/lib/vpa.ts';

import { cpuShort, memShort, outside } from '@/lib/vpa.ts';

const props = defineProps<{ recommendations: Recommendation[]; requests?: ContainerRequests[] }>();

interface Line {
    key: string;
    container: string;
    first: boolean;
    res: 'CPU' | 'MEM';
    request: string;
    warn: boolean;
    target: string;
    lower: string;
    upper: string;
    uncapped: string;
}

const lines = computed<Line[]>(() => {
    const out: Line[] = [];
    for (const r of props.recommendations) {
        const req = props.requests?.find(q => q.container === r.container);
        for (const k of ['cpu', 'mem'] as const) {
            const fmt = k === 'cpu' ? cpuShort : memShort;
            const at = (x: Res) => fmt(x[k]);
            out.push({
                key: `${r.container}/${k}`,
                container: r.container,
                first: k === 'cpu',
                res: k === 'cpu' ? 'CPU' : 'MEM',
                request: req ? at(req) : '—',
                warn: !!req && outside(req[k], r.lower[k], r.upper[k]),
                target: at(r.target),
                lower: at(r.lower),
                upper: at(r.upper),
                uncapped: at(r.uncapped)
            });
        }
    }
    return out;
});
</script>

<template>
    <table class="ctable" data-testid="vpa-recs">
        <thead>
            <tr>
                <th>Container</th>
                <th></th>
                <th v-if="requests" class="n">Request</th>
                <th class="n">Target</th>
                <th class="n">Lower</th>
                <th class="n">Upper</th>
                <th class="n" title="The target before the VPA's min/max allowed policy was applied">Uncapped</th>
            </tr>
        </thead>
        <tbody>
            <tr v-for="l in lines" :key="l.key" :class="{ first: l.first }">
                <td>{{ l.first ? l.container : '' }}</td>
                <td class="muted">{{ l.res }}</td>
                <td v-if="requests" class="n" :class="{ warn: l.warn }" :title="l.warn ? 'The request is outside the recommended range' : undefined">
                    {{ l.request }}
                </td>
                <td class="n">{{ l.target }}</td>
                <td class="n">{{ l.lower }}</td>
                <td class="n">{{ l.upper }}</td>
                <td class="n muted">{{ l.uncapped }}</td>
            </tr>
        </tbody>
    </table>
</template>

<style scoped>
.ctable {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
}
.ctable th {
    text-align: left;
    font-size: 11px;
    font-weight: 600;
    color: var(--text3);
    padding: 4px 6px;
    border-bottom: 1px solid var(--border);
}
.ctable td {
    padding: 3px 6px;
    vertical-align: top;
}
.ctable tr.first td {
    border-top: 1px solid var(--border);
    padding-top: 6px;
}
.ctable tbody tr:first-child td {
    border-top: 0;
}
.ctable .n {
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
}
.warn {
    color: var(--warning);
    font-weight: 600;
}
</style>
