import { computed, inject } from '@angular/core';
import { patchState, signalStore, withComputed, withMethods, withState } from '@ngrx/signals';
import { firstValueFrom } from 'rxjs';

import type { FaasboxFunction } from '@/models/faasbox-function.model';
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
      return id ? functions().find((f) => f.id === id) ?? null : null;
    }),
    sortedFunctions: computed(() =>
      functions()
        .slice()
        .sort((a, b) => a.name.localeCompare(b.name)),
    ),
  })),
  withMethods((store, functionsService = inject(FunctionsService)) => ({
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
      const defaultScript = `const input = await Bun.stdin.json();\n\nconst result = { message: "Hello from ${name}!" };\n\nconsole.log(JSON.stringify(result));\n`;
      const created = await firstValueFrom(
        functionsService.create({ name, script: defaultScript, packageJson: '' }),
      );
      patchState(store, { functions: [...store.functions(), created], selectedId: created.id });
      return created;
    },

    async updateFunction(
      id: string,
      data: Partial<Pick<FaasboxFunction, 'name' | 'script' | 'packageJson'> & { plainEnv?: Record<string, string> }>,
    ): Promise<FaasboxFunction> {
      const updated = await firstValueFrom(functionsService.update(id, data));
      patchState(store, {
        functions: store.functions().map((f) => (f.id === id ? updated : f)),
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
  })),
);
