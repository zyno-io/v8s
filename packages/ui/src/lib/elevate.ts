/** The one menu item a cluster's context menu offers (null: nothing to offer, the cluster has no linked write context). */
export interface ElevateItem {
    label: string;
    hint: string;
    /** The `elevated` argument of `clusters.elevate`. */
    elevate: boolean;
}

export function elevateItem(name: string, writeCtx: string, elevated: boolean): ElevateItem | null {
    if (elevated) return { label: `Revert to ${name}`, hint: "Back to the read context. This cluster's data reloads.", elevate: false };
    if (!writeCtx) return null;
    return {
        label: `Elevate to ${writeCtx}`,
        hint: `Reads, watches and writes run as ${writeCtx}. This cluster's data reloads.`,
        elevate: true
    };
}

/**
 * The word for a switch in flight: `target` is the state the user asked for (undefined: none requested), `elevated` the state the
 * agent reports. Empty once the agent reports the target, so the busy look ends the moment the cluster flips.
 */
export function switchingWord(target: boolean | undefined, elevated: boolean): '' | 'elevating' | 'reverting' {
    if (target === undefined || target === elevated) return '';
    return target ? 'elevating' : 'reverting';
}
