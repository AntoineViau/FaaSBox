import { computed, effect, Injectable, signal } from '@angular/core';

export type Theme = 'light' | 'dark';

/**
 * Also hardcoded in the inline script of `ui/src/index.html`, which applies the
 * theme before the first paint. That script cannot import anything, so the key
 * and the initial choice rule below are duplicated there. Change one, change
 * the other, or the flash of the wrong theme comes back silently.
 */
export const THEME_STORAGE_KEY = 'faasbox-theme';

function initialTheme(): Theme {
  const stored = localStorage.getItem(THEME_STORAGE_KEY);
  if (stored === 'light' || stored === 'dark') return stored;
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

@Injectable({ providedIn: 'root' })
export class ThemeService {
  readonly theme = signal<Theme>(initialTheme());
  readonly isDark = computed(() => this.theme() === 'dark');

  constructor() {
    effect(() => {
      const theme = this.theme();
      localStorage.setItem(THEME_STORAGE_KEY, theme);
      document.documentElement.classList.toggle('dark', theme === 'dark');
    });
  }

  toggle(): void {
    this.theme.update((current) => (current === 'dark' ? 'light' : 'dark'));
  }
}
