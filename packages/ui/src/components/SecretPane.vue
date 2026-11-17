<script setup lang="ts">
import type { ObjectPayload, ObjectRef } from '@v8s/client';

import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { errorText } from '@/lib/mutations.ts';

import YamlPane from './YamlPane.vue';

const props = defineProps<{ target: ObjectRef }>();

// The payload (and with it any revealed values) lives in a shallowRef, not Pinia and not a deep ref. It is dropped when this
// pane goes away (drawer closed, tab changed) or the object changes.
const payload = shallowRef<ObjectPayload | null>(null);
const revealed = ref(false);
const loading = ref(false);
const error = ref('');
const pane = ref<{ mode: string } | null>(null);
let seq = 0;

async function load(reveal: boolean) {
    const my = ++seq;
    loading.value = true;
    error.value = '';
    try {
        const t = props.target;
        const res = await getAgent().call<ObjectPayload>('object.get', {
            cluster: t.cluster,
            gvr: t.gvr,
            namespace: t.namespace,
            name: t.name,
            reveal: reveal || undefined
        });
        if (my !== seq) return;
        payload.value = res;
        revealed.value = reveal;
    } catch (e) {
        if (my !== seq) return;
        error.value = errorText(e);
        // Never keep values around after a failed refetch of a masked view.
        if (!reveal) payload.value = null;
    } finally {
        if (my === seq) loading.value = false;
    }
}

function drop() {
    seq++;
    payload.value = null;
    revealed.value = false;
}

onMounted(() => void load(false));
onBeforeUnmount(drop);
watch(
    () => `${props.target.cluster}|${props.target.namespace}|${props.target.name}`,
    () => {
        drop();
        void load(false);
    }
);

const editing = () => !!pane.value && pane.value.mode !== 'view';
</script>

<template>
    <div class="spane ypane">
        <p v-if="error" class="notice danger" role="alert">{{ error }}</p>
        <p v-if="loading && !payload" class="muted" role="status">Loading…</p>
        <template v-if="payload">
            <div class="bar">
                <span v-if="revealed" class="tag warning" role="status">values revealed</span>
                <span v-else class="tag" role="status">values masked</span>
                <div class="grow"></div>
                <button
                    v-if="!revealed"
                    class="btn sm"
                    type="button"
                    data-testid="reveal"
                    :disabled="loading || editing()"
                    :title="editing() ? 'Finish or discard your edit first' : 'Fetch the Secret again with its values'"
                    @click="load(true)"
                >
                    Reveal all
                </button>
                <button v-else class="btn sm" type="button" data-testid="mask" :disabled="loading || editing()" @click="load(false)">
                    Mask again
                </button>
            </div>
            <YamlPane ref="pane" :target="target" :payload="payload" :deleted="false" :frames="1" />
        </template>
    </div>
</template>

<style scoped>
.spane {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-height: 0;
    flex: 1 1 0;
}
.bar {
    display: flex;
    align-items: center;
    gap: 8px;
}
</style>
