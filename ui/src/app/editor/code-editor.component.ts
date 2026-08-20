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
import { acceptCompletion } from '@codemirror/autocomplete';
import { indentWithTab } from '@codemirror/commands';
import { indentUnit } from '@codemirror/language';
import { linter } from '@codemirror/lint';
import { Compartment, EditorState, type Extension } from '@codemirror/state';
import { keymap } from '@codemirror/view';
import { EditorView, basicSetup } from 'codemirror';
import { javascript } from '@codemirror/lang-javascript';
import { json, jsonParseLinter } from '@codemirror/lang-json';
import { oneDark } from '@codemirror/theme-one-dark';

import { faasboxCompletions } from './faasbox-snippets';
import { ThemeService } from '@/theme/theme.service';

@Component({
  selector: 'app-code-editor',
  standalone: true,
  template: `<div #editorHost class="h-full w-full overflow-auto"></div>`,
  // An Angular host is inline by default, and a percentage height against an
  // inline box resolves to auto: without this, the div below grows to the
  // height of the whole document instead of scrolling inside its panel.
  styles: `
    :host {
      display: block;
      height: 100%;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CodeEditorComponent implements OnDestroy {
  readonly content = input('');
  readonly language = input<'typescript' | 'json'>('typescript');
  /**
   * Shows the document without offering to change it. It is what a read-only
   * instance binds; the component itself knows nothing of the mode.
   */
  readonly readOnly = input(false);
  readonly contentChange = output<string>();

  private readonly themeService = inject(ThemeService);

  private readonly editorHost = viewChild.required<ElementRef>('editorHost');
  private view: EditorView | null = null;
  private readonly languageCompartment = new Compartment();
  private readonly themeCompartment = new Compartment();
  private readonly readOnlyCompartment = new Compartment();
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
          effects: this.languageCompartment.reconfigure(this.languageExtension(lang)),
        });
      }
    });

    // A compartment like the language and the theme: the state is built once,
    // and reconfiguring is the only way to change a facet afterwards.
    effect(() => {
      const readOnly = this.readOnly();
      if (this.view) {
        this.view.dispatch({
          effects: this.readOnlyCompartment.reconfigure(EditorState.readOnly.of(readOnly)),
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

  /**
   * The language extensions of a tab, in one place because two callers need
   * them: the initial state and the reconfiguration effect.
   *
   * The JSON linter lives here rather than in the static extensions because it
   * has to come and go with the language. The completion source likewise: the
   * package.json tab must never see the `faasbox-*` entries.
   */
  private languageExtension(lang: 'typescript' | 'json'): Extension {
    return lang === 'json'
      ? [json(), linter(jsonParseLinter())]
      : [javascript({ typescript: true }), faasboxCompletions];
  }

  private initEditor(): void {
    const startState = EditorState.create({
      doc: this.content(),
      extensions: [
        // basicSetup leaves Tab alone — a deliberate omission of CodeMirror,
        // since binding it traps the keyboard inside the editor. We take that
        // trap knowingly: Ctrl-m (Shift-Alt-m on Mac), bound by defaultKeymap
        // to toggleTabFocusMode, hands Tab back to navigation, and the docs
        // say so.
        //
        // Placed before basicSetup because facet precedence follows the order
        // of the array — that is what puts us ahead of anything it binds. It
        // does not put us ahead of everything: the snippet field keymap is
        // registered at Prec.highest, so Tab keeps walking the fields of an
        // inserted snippet before reaching us. That order is the one we want.
        //
        // Order matters inside the array too: buildKeymap stacks the commands
        // of one key and stops at the first that answers, so an open
        // completion popup takes Tab before indentation does.
        keymap.of([{ key: 'Tab', run: acceptCompletion }, indentWithTab]),
        // Already the default of the facet. Restated because Tab is now a
        // visible user command, and what it inserts stops being an internal
        // detail of @codemirror/language.
        indentUnit.of('  '),
        basicSetup,
        this.themeCompartment.of(this.themeService.isDark() ? oneDark : []),
        this.languageCompartment.of(this.languageExtension(this.language())),
        this.readOnlyCompartment.of(EditorState.readOnly.of(this.readOnly())),
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
