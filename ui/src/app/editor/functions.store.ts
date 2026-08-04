import { computed, inject } from '@angular/core';
import { patchState, signalStore, withComputed, withMethods, withState } from '@ngrx/signals';
import { firstValueFrom } from 'rxjs';

import type { DepsStateMessage, FaasboxFunction } from '@/models/faasbox-function.model';
import { FunctionsService } from '@/editor/functions.service';

type FunctionsState = {
  functions: FaasboxFunction[];
  selectedId: string | null;
  isLoading: boolean;
  error: string | null;
};

const initialState: FunctionsState = {
  functions: [],
  selectedId: null,
  isLoading: false,
  error: null,
};

export const FunctionsStore = signalStore(
  { providedIn: 'root' },
  withState(initialState),
  withComputed(({ functions, selectedId }) => ({
    selectedFunction: computed(() => {
      const id = selectedId();
      return id ? (functions().find((f) => f.id === id) ?? null) : null;
    }),
    sortedFunctions: computed(() =>
      functions()
        .slice()
        .sort((a, b) => a.name.localeCompare(b.name)),
    ),
  })),
  withMethods((store, functionsService = inject(FunctionsService)) => {
    /**
     * Writes the two dependency fields on one function and nothing else. The
     * event carries only those two, and replacing the whole record with it would
     * discard a script or a package.json the store holds fresher.
     */
    const applyDepsState = (state: DepsStateMessage): void => {
      patchState(store, {
        functions: store
          .functions()
          .map((f) =>
            f.name === state.functionName
              ? { ...f, depsStatus: state.depsStatus, depsError: state.depsError }
              : f,
          ),
      });
    };

    /**
     * Re-reads the dependency state of every function. Messages emitted while
     * the stream was down are gone, so a reconnection that only resumed
     * listening would keep an "installing" nothing will ever lift. Only the two
     * fields are taken from the fresh copy — the rest of each record may hold
     * edits this reload has no business undoing.
     */
    const refreshDepsState = async (): Promise<void> => {
      const res = await firstValueFrom(functionsService.list());
      const fresh = new Map(res.items.map((f) => [f.id, f]));
      patchState(store, {
        functions: store.functions().map((f) => {
          const latest = fresh.get(f.id);
          return latest
            ? { ...f, depsStatus: latest.depsStatus, depsError: latest.depsError }
            : f;
        }),
      });
    };

    return {
      applyDepsState,
      refreshDepsState,

      /**
       * Opens the dependency state channel and returns the function that closes
       * it. The caller owns the lifetime; the store owns the state it lands in.
       */
      startDepsStateSync(): () => void {
        return functionsService.subscribeDepsState(applyDepsState, () => {
          void refreshDepsState();
        });
      },

      async loadFunctions(): Promise<void> {
        patchState(store, { isLoading: true, error: null });
        try {
          const res = await firstValueFrom(functionsService.list());
          patchState(store, { functions: res.items, isLoading: false });
        } catch (e) {
          patchState(store, { isLoading: false, error: (e as Error).message });
        }
      },

      selectFunction(id: string | null): void {
        patchState(store, { selectedId: id });
      },

      async createFunction(name: string): Promise<FaasboxFunction> {
        const defaultScript = `const payload = await Bun.stdin.json();\n\nconst result = { message: "Hello from ${name}!" };\n\nconsole.log(JSON.stringify(result));\n`;
        const created = await firstValueFrom(
          functionsService.create({ name, script: defaultScript, packageJson: '' }),
        );
        patchState(store, { functions: [...store.functions(), created], selectedId: created.id });
        return created;
      },

      async updateFunction(
        id: string,
        data: Partial<
          Pick<FaasboxFunction, 'name' | 'script' | 'packageJson'> & {
            plainEnv?: Record<string, string>;
          }
        >,
      ): Promise<FaasboxFunction> {
        const updated = await firstValueFrom(functionsService.update(id, data));
        patchState(store, {
          functions: store.functions().map((f) =>
            f.id === id
              ? // The dependency fields keep whatever the store holds. The install
                // is scheduled synchronously by the save hook, so this response
                // left the server carrying "pending" — and the channel has very
                // likely pushed "installing" past it before this promise settled.
                // Applying the response wholesale would walk the display backwards.
                { ...updated, depsStatus: f.depsStatus, depsError: f.depsError }
              : f,
          ),
        });
        return updated;
      },

      async deleteFunction(id: string): Promise<void> {
        await firstValueFrom(functionsService.delete(id));
        const remaining = store.functions().filter((f) => f.id !== id);
        patchState(store, {
          functions: remaining,
          selectedId: store.selectedId() === id ? null : store.selectedId(),
        });
      },
    };
  }),
);
