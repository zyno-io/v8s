import type { ObjectPayload, ObjectRef, ObjectSubscription } from '@v8s/client';
import type { Ref } from 'vue';

import { onScopeDispose, ref, shallowRef, watch } from 'vue';

import { getAgent } from './agent.ts';
import { errorText } from './mutations.ts';

export type ParsedObject = Record<string, unknown>;

const keyOf = (r: ObjectRef | null) => (r ? `${r.cluster}|${r.gvr}|${r.namespace ?? ''}|${r.name ?? ''}` : '');

/**
 * Live state of one open object: `object.subscribe` frames in, a parsed copy for the Overview out. The payload is a single
 * object (a few KB of YAML), so a shallow ref and a main-thread parse are fine; the YAML parser loads lazily.
 */
export function useObjectSession(target: Readonly<Ref<ObjectRef | null>>) {
    const payload = shallowRef<ObjectPayload | null>(null);
    const parsed = shallowRef<ParsedObject | null>(null);
    const deleted = ref(false);
    const error = ref('');
    const loading = ref(false);
    /** Bumped on every server frame after the first; the editor compares resourceVersions itself. */
    const frames = ref(0);

    let sub: ObjectSubscription | null = null;
    let seq = 0;

    function close() {
        sub?.close();
        sub = null;
    }

    async function parse(p: ObjectPayload | null, my: number) {
        if (!p) {
            parsed.value = null;
            return;
        }
        try {
            const { load } = await import('js-yaml');
            if (my !== seq) return;
            const doc = load(p.yaml);
            parsed.value = doc && typeof doc === 'object' ? (doc as ParsedObject) : null;
        } catch {
            parsed.value = null;
        }
    }

    watch(
        () => keyOf(target.value),
        async () => {
            close();
            payload.value = null;
            parsed.value = null;
            deleted.value = false;
            error.value = '';
            frames.value = 0;
            const ref0 = target.value;
            if (!ref0) {
                loading.value = false;
                return;
            }
            const my = ++seq;
            loading.value = true;
            try {
                const s = await getAgent().subscribeObject(ref0, frame => {
                    if (my !== seq) return;
                    loading.value = false;
                    if (frame.error) {
                        error.value = frame.error.message || frame.error.code;
                        return;
                    }
                    if (frame.deleted) deleted.value = true;
                    if (frame.yaml !== undefined) {
                        error.value = '';
                        payload.value = frame;
                        frames.value++;
                        void parse(frame, my);
                    }
                });
                if (my !== seq) s.close();
                else sub = s;
            } catch (e) {
                if (my === seq) {
                    loading.value = false;
                    error.value = errorText(e);
                }
            }
        },
        { immediate: true }
    );

    onScopeDispose(() => {
        seq++;
        close();
    });

    return { payload, parsed, deleted, error, loading, frames };
}
