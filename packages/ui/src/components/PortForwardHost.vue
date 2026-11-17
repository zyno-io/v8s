<script setup lang="ts">
import type { ObjectRef } from '@v8s/client';

import { ref, watch } from 'vue';

import PortForwardDialog from '@/components/PortForwardDialog.vue';
import { getAgent } from '@/lib/agent.ts';
import { errorText } from '@/lib/mutations.ts';
import { portForwardRequest } from '@/lib/portforward.ts';
import { useUiStore } from '@/stores/ui.ts';

const ui = useUiStore();
const target = ref<ObjectRef | null>(null);
const parsed = ref<Record<string, unknown> | null>(null);
let seq = 0;

watch(portForwardRequest, async req => {
    const my = ++seq;
    target.value = null;
    parsed.value = null;
    if (!req) return;
    let doc: Record<string, unknown> | null = null;
    try {
        const res = await getAgent().call<{ yaml: string }>('object.get', {
            cluster: req.cluster,
            gvr: req.gvr,
            namespace: req.namespace,
            name: req.name
        });
        const { load } = await import('js-yaml');
        const v = load(res.yaml);
        doc = v && typeof v === 'object' ? (v as Record<string, unknown>) : null;
    } catch (e) {
        if (my === seq) ui.toast('danger', `Could not load ports: ${errorText(e)}`);
        // Fall through: the dialog still offers a custom port.
    }
    if (my !== seq) return;
    parsed.value = doc;
    target.value = req;
});

function close() {
    seq++;
    target.value = null;
    portForwardRequest.value = null;
}
</script>

<template>
    <PortForwardDialog
        v-if="target"
        :key="`${target.cluster}/${target.gvr}/${target.namespace}/${target.name}`"
        :target="target"
        :parsed="parsed"
        @close="close"
    />
</template>
