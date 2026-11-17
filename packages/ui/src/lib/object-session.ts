import type { ObjectPayload, ObjectRef, ObjectSubscription } from '@v8s/client';
import type { Ref } from 'vue';

import { onScopeDispose, ref, shallowRef, watch } from 'vue';

import { getAgent } from './agent.ts';
import { errorText } from './mutations.ts';

export type ParsedObject = Record<string, unknown>;

/** Documents above this many characters are not parsed on the main thread; the Overview shows what it can without them. */
export const PARSE_LIMIT = 256 * 1024;
const PARSE_DEBOUNCE_MS = 100;

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
    let parseTimer: ReturnType<typeof setTimeout> | undefined;

    function close() {
        sub?.close();
        sub = null;
    }

    /** Debounced: a burst of frames parses once, for the latest one. */
    function scheduleParse(p: ObjectPayload, my: number) {
        clearTimeout(parseTimer);
        if (p.yaml.length > PARSE_LIMIT) {
            parsed.value = null;
            return;
        }
        parseTimer = setTimeout(() => void parse(p, my), PARSE_DEBOUNCE_MS);
    }

    async function parse(p: ObjectPayload, my: number) {
        try {
            const { load } = await import('js-yaml');
            if (my !== seq) return;
            const doc = load(p.yaml);
            if (my !== seq) return;
            parsed.value = doc && typeof doc === 'object' ? (doc as ParsedObject) : null;
        } catch {
            parsed.value = null;
        }
    }

    watch(
        () => keyOf(target.value),
        async () => {
            close();
            clearTimeout(parseTimer);
            payload.value = null;
            parsed.value = null;
            deleted.value = false;
            error.value = '';
            frames.value = 0;
            const my = ++seq;
            const ref0 = target.value;
            if (!ref0) {
                loading.value = false;
                return;
            }
            loading.value = true;
            try {
                const s = await getAgent().subscribeObject(ref0, frame => {
                    if (my !== seq) return;
                    loading.value = false;
                    if (frame.error) {
                        error.value = frame.error.message || frame.error.code;
                        return;
                    }
                    deleted.value = !!frame.deleted;
                    if (frame.yaml !== undefined) {
                        error.value = '';
                        payload.value = frame;
                        frames.value++;
                        scheduleParse(frame, my);
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
        clearTimeout(parseTimer);
        close();
    });

    return { payload, parsed, deleted, error, loading, frames };
}
