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
    /**
     * The order the sidebar shows, and the only one there is: the name is
     * encrypted at rest, so the server settles nothing. This sort was already
     * what the list was displayed in — it merely stopped being a second opinion.
     */
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
     *
     * The match is on the record id, and on nothing else. A save that renames
     * publishes its state under the new name before the response carrying that
     * name gets back here, so matching on the name loses those messages — and
     * when the same save empties package.json, the lost message is the only one
     * there will ever be, leaving a stale state on screen. Every writer on the
     * server holds the id, so there is no message left for a fallback to catch.
     */
    const applyDepsState = (state: DepsStateMessage): void => {
      patchState(store, {
        functions: store
          .functions()
          .map((f) =>
            f.id === state.functionId
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
        // The template is the first thing anyone reads about the contract, so it
        // shows the whole of it at once: the body, a header, a secret and the
        // trigger, one variable each, gathered into the answer on the last line.
        // It runs as it stands — the Runner starts on a body and a header that
        // feed it, and the function is created with the variable it reads.
        const defaultScript = `// The whole call arrives on stdin, in a single envelope.
const req = JSON.parse(await Bun.stdin.text());
const body = JSON.parse(req.body || "{}");

const visitor = body.name;
const header = req.headers?.["x-header-name"]; // header names are lowercased
const secret = process.env.SECRET; // see Environment tab — returning it puts it in the logs
const trigger = req.trigger; // "http", "cron", "startup" or "mcp"

console.log(JSON.stringify({ message: "Hello from ${name}!", visitor, header, secret, trigger }));
`;
        const created = await firstValueFrom(
          // The demo variable ships with the function so the template runs on the
          // first click rather than reading undefined. Its value is a stand-in,
          // and it deletes like any other, from the Environment tab.
          functionsService.create({
            name,
            script: defaultScript,
            packageJson: '',
            plainEnv: { SECRET: 'a-secret-value' },
          }),
        );
        // The selection is not touched here: it belongs to the URL, and the
        // editor navigates to the new function so the address bar follows.
        patchState(store, { functions: [...store.functions(), created] });
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
        // The selection is not cleared here: it belongs to the URL, and clearing
        // it would be redundant anyway - selectedFunction resolves to null as
        // soon as the record leaves the list, and the editor navigates back to
        // the empty state on an id nothing answers to.
        patchState(store, { functions: store.functions().filter((f) => f.id !== id) });
      },
    };
  }),
);
