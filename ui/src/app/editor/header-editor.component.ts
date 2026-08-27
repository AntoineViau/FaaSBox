import { ChangeDetectionStrategy, Component, input, model } from '@angular/core';

import { headerNotice, type HeaderRow } from '@/editor/request-headers';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

/**
 * The key/value rows the Runner puts on its request.
 *
 * Presentational and nothing else: it owns no request, knows no function, and
 * hands the rows back through a two-way binding. It sits beside the body for
 * one reason — a signed webhook signs a header, so testing one from the editor
 * was impossible while headers could not be typed.
 *
 * The warning is inline, under the row that earned it. Collected in a note at
 * the bottom of the panel it would be read once and then never again, which is
 * precisely when it is needed: at the moment `Authorization` is being typed.
 */
@Component({
  selector: 'app-header-editor',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent, ZardInputDirective],
  template: `
    <div class="px-3 py-2">
      <div class="mb-1.5 text-xs text-muted-foreground">Headers</div>

      @for (row of headers(); track $index) {
        <div class="mb-1 flex items-center gap-1.5">
          <input
            z-input
            type="text"
            placeholder="Name"
            class="h-7 flex-1 font-mono text-xs"
            [value]="row.name"
            [disabled]="readOnly()"
            (input)="setName($index, $any($event.target).value)"
          />
          <input
            z-input
            type="text"
            placeholder="Value"
            class="h-7 flex-[2] font-mono text-xs"
            [value]="row.value"
            [disabled]="readOnly()"
            (input)="setValue($index, $any($event.target).value)"
          />
          <button
            z-button
            zType="ghost"
            zSize="icon"
            class="h-7 w-7 shrink-0"
            [disabled]="readOnly()"
            (click)="remove($index)"
          >
            <z-icon zType="trash" class="h-3 w-3 text-destructive" />
          </button>
        </div>
        @if (notice(row.name); as text) {
          <p class="mb-1.5 pl-1 text-xs text-yellow-500">{{ text }}</p>
        }
      } @empty {
        <p class="mb-1.5 text-xs text-muted-foreground">No header.</p>
      }

      <button
        z-button
        zType="outline"
        zSize="sm"
        class="h-7"
        [disabled]="readOnly()"
        (click)="add()"
      >
        <z-icon zType="plus" class="mr-1.5 h-3.5 w-3.5" />
        Add header
      </button>
    </div>
  `,
  // An Angular host is inline by default, and this one fills the pane it is
  // given — a height an inline box would ignore.
  styles: `
    :host {
      display: block;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HeaderEditorComponent {
  /** The rows, owned by the caller: they are what it puts on its request. */
  readonly headers = model.required<HeaderRow[]>();
  /**
   * Shows the rows without offering to change them. Like the code editor, the
   * component knows nothing of the mode that binds it.
   */
  readonly readOnly = input(false);

  protected readonly notice = headerNotice;

  protected add(): void {
    if (this.readOnly()) return;
    this.headers.update((rows) => [...rows, { name: '', value: '' }]);
  }

  protected remove(index: number): void {
    if (this.readOnly()) return;
    this.headers.update((rows) => rows.filter((_, i) => i !== index));
  }

  protected setName(index: number, name: string): void {
    this.headers.update((rows) => rows.map((row, i) => (i === index ? { ...row, name } : row)));
  }

  protected setValue(index: number, value: string): void {
    this.headers.update((rows) => rows.map((row, i) => (i === index ? { ...row, value } : row)));
  }
}
