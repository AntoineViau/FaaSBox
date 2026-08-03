import {
  afterNextRender,
  ChangeDetectionStrategy,
  Component,
  effect,
  type ElementRef,
  inject,
  input,
  type OnDestroy,
  output,
  viewChild,
} from '@angular/core';
import { Compartment, EditorState } from '@codemirror/state';
import { EditorView, basicSetup } from 'codemirror';
import { javascript } from '@codemirror/lang-javascript';
import { json } from '@codemirror/lang-json';
import { oneDark } from '@codemirror/theme-one-dark';

import { ThemeService } from '@/theme/theme.service';

@Component({
  selector: 'app-code-editor',
  standalone: true,
  template: `<div #editorHost class="h-full w-full overflow-auto"></div>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CodeEditorComponent implements OnDestroy {
  readonly content = input('');
  readonly language = input<'typescript' | 'json'>('typescript');
  readonly contentChange = output<string>();

  private readonly themeService = inject(ThemeService);

  private readonly editorHost = viewChild.required<ElementRef>('editorHost');
  private view: EditorView | null = null;
  private readonly languageCompartment = new Compartment();
  private readonly themeCompartment = new Compartment();
  private suppressNextUpdate = false;

  constructor() {
    afterNextRender(() => this.initEditor());

    // The light theme is the absence of an extension: basicSetup is light by
    // default, so nothing has to be installed for it.
    effect(() => {
      const dark = this.themeService.isDark();
      if (this.view) {
        this.view.dispatch({
          effects: this.themeCompartment.reconfigure(dark ? oneDark : []),
        });
      }
    });

    effect(() => {
      const lang = this.language();
      if (this.view) {
        this.view.dispatch({
          effects: this.languageCompartment.reconfigure(
            lang === 'json' ? json() : javascript({ typescript: true }),
          ),
        });
      }
    });

    effect(() => {
      const value = this.content();
      if (this.view && this.view.state.doc.toString() !== value) {
        this.suppressNextUpdate = true;
        this.view.dispatch({
          changes: { from: 0, to: this.view.state.doc.length, insert: value },
        });
      }
    });
  }

  private initEditor(): void {
    const lang = this.language();
    const startState = EditorState.create({
      doc: this.content(),
      extensions: [
        basicSetup,
        this.themeCompartment.of(this.themeService.isDark() ? oneDark : []),
        this.languageCompartment.of(
          lang === 'json' ? json() : javascript({ typescript: true }),
        ),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            if (this.suppressNextUpdate) {
              this.suppressNextUpdate = false;
              return;
            }
            this.contentChange.emit(update.state.doc.toString());
          }
        }),
      ],
    });

    this.view = new EditorView({
      state: startState,
      parent: this.editorHost().nativeElement,
    });
  }

  ngOnDestroy(): void {
    this.view?.destroy();
  }
}
