import { ChangeDetectionStrategy, Component, inject } from '@angular/core';

import { ThemeService } from '@/theme/theme.service';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

@Component({
  selector: 'app-theme-toggle',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent],
  template: `
    <button
      z-button
      zType="outline"
      zSize="sm"
      [attr.aria-label]="isDark() ? 'Switch to light theme' : 'Switch to dark theme'"
      [attr.title]="isDark() ? 'Switch to light theme' : 'Switch to dark theme'"
      (click)="toggle()"
    >
      <z-icon [zType]="isDark() ? 'sun' : 'moon'" class="h-4 w-4" />
    </button>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ThemeToggleComponent {
  private readonly themeService = inject(ThemeService);

  protected readonly isDark = this.themeService.isDark;

  protected toggle(): void {
    this.themeService.toggle();
  }
}
