import {
  ChangeDetectionStrategy,
  Component,
  computed,
  HostListener,
  inject,
  input,
  signal,
} from '@angular/core';

import { AuthService } from '@/auth/auth.service';
import { errorText } from '@/editor/error-text';
import { DEMO_MODE_HINT } from '@/instance/instance.service';
import { ZardAlertComponent } from '@shared/components/alert';
import { ZardButtonComponent } from '@shared/components/button';
import {
  ZardFormControlComponent,
  ZardFormFieldComponent,
  ZardFormLabelComponent,
} from '@shared/components/form';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

/**
 * The header button that changes the superuser's password, and its dialog.
 *
 * Like the theme toggle, it is a header widget that talks to its own service:
 * passing the call up through an `output()` would wire the same handler into
 * every page that has a header.
 *
 * The dialog is written here, after the invoke hint's: there is still no shared
 * dialog component, and two callers are not yet the moment to vendor one.
 */
@Component({
  selector: 'app-change-password',
  standalone: true,
  imports: [
    ZardAlertComponent,
    ZardButtonComponent,
    ZardFormControlComponent,
    ZardFormFieldComponent,
    ZardFormLabelComponent,
    ZardIconComponent,
    ZardInputDirective,
  ],
  template: `
    <!-- The demo hint goes on the wrapper: a disabled button gets no hover
         event. The button drops its own title then, so the hint is the one
         that shows. -->
    <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
      <button
        z-button
        zType="outline"
        zSize="sm"
        aria-label="Change password"
        [attr.title]="demoMode() ? null : 'Change password'"
        [zDisabled]="demoMode()"
        (click)="open()"
      >
        <z-icon zType="user" class="h-4 w-4" />
      </button>
    </span>

    @if (dialogOpen()) {
      <div
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
        role="dialog"
        aria-modal="true"
        aria-labelledby="change-password-title"
      >
        <div class="w-full max-w-sm rounded-lg border border-border bg-background p-4 shadow-lg">
          <h2 id="change-password-title" class="text-sm font-semibold">Change password</h2>

          @if (done()) {
            <p class="mt-3 text-sm">
              Password changed. Every other session — other browsers, the PocketBase admin — is
              signed out.
            </p>
            <div class="mt-4 flex justify-end">
              <button z-button zSize="sm" type="button" (click)="close()">Close</button>
            </div>
          } @else {
            <form class="mt-3 flex flex-col gap-4" (submit)="submit($event)">
              @if (errorMessage()) {
                <z-alert zType="destructive" zTitle="Error" [zDescription]="errorMessage()" />
              }

              <z-form-field>
                <label z-form-label zRequired for="change-password-current">Current password</label>
                <z-form-control>
                  <input
                    z-input
                    id="change-password-current"
                    type="password"
                    autocomplete="current-password"
                    [value]="currentPassword()"
                    [disabled]="loading()"
                    (input)="currentPassword.set($any($event.target).value)"
                  />
                </z-form-control>
              </z-form-field>

              <z-form-field>
                <label z-form-label zRequired for="change-password-new">New password</label>
                <z-form-control>
                  <input
                    z-input
                    id="change-password-new"
                    type="password"
                    autocomplete="new-password"
                    [value]="newPassword()"
                    [disabled]="loading()"
                    (input)="newPassword.set($any($event.target).value)"
                  />
                </z-form-control>
              </z-form-field>

              <z-form-field>
                <label z-form-label zRequired for="change-password-confirm">
                  Confirm new password
                </label>
                <z-form-control
                  [errorMessage]="mismatch() ? 'The new passwords do not match.' : ''"
                >
                  <input
                    z-input
                    id="change-password-confirm"
                    type="password"
                    autocomplete="new-password"
                    [value]="confirmPassword()"
                    [disabled]="loading()"
                    (input)="confirmPassword.set($any($event.target).value)"
                  />
                </z-form-control>
              </z-form-field>

              <div class="flex justify-end gap-2">
                <button
                  z-button
                  zType="outline"
                  zSize="sm"
                  type="button"
                  [zDisabled]="loading()"
                  (click)="close()"
                >
                  Cancel
                </button>
                <button
                  z-button
                  zSize="sm"
                  type="submit"
                  [zLoading]="loading()"
                  [zDisabled]="!canSubmit()"
                >
                  Change password
                </button>
              </div>
            </form>
          }
        </div>
      </div>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangePasswordComponent {
  private readonly authService = inject(AuthService);

  /** A showcase keeps the button in sight and closes it. */
  readonly demoMode = input(false);

  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  protected readonly dialogOpen = signal(false);
  protected readonly loading = signal(false);
  protected readonly done = signal(false);
  protected readonly errorMessage = signal('');

  protected readonly currentPassword = signal('');
  protected readonly newPassword = signal('');
  protected readonly confirmPassword = signal('');

  /**
   * Only once a confirmation is typed: flagging the empty field the moment the
   * new password is entered would scold the user for a step not yet taken.
   */
  protected readonly mismatch = computed(
    () => this.confirmPassword() !== '' && this.confirmPassword() !== this.newPassword(),
  );

  protected readonly canSubmit = computed(
    () =>
      this.currentPassword() !== '' &&
      this.newPassword() !== '' &&
      this.confirmPassword() !== '' &&
      !this.mismatch() &&
      !this.loading(),
  );

  protected open(): void {
    if (this.demoMode()) return;
    this.dialogOpen.set(true);
  }

  /** Leaves nothing of a password behind, whichever way the dialog closes. */
  protected close(): void {
    this.dialogOpen.set(false);
    this.done.set(false);
    this.errorMessage.set('');
    this.clearFields();
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    if (!this.canSubmit()) return;

    this.loading.set(true);
    this.errorMessage.set('');
    try {
      await this.authService.changePassword(this.currentPassword(), this.newPassword());
      this.clearFields();
      this.done.set(true);
    } catch (error) {
      // The dialog stays open with what was typed, so a typo is fixed in place.
      this.errorMessage.set(errorText(error));
    } finally {
      this.loading.set(false);
    }
  }

  // On the document rather than the host: the dialog is a modal overlay, and
  // Escape has to reach it wherever the focus sits. Not while a change is in
  // flight — between the PATCH and the new sign-in, the tab holds a dead token.
  @HostListener('document:keydown.escape')
  protected onEscape(): void {
    if (!this.dialogOpen() || this.loading()) return;
    this.close();
  }

  private clearFields(): void {
    this.currentPassword.set('');
    this.newPassword.set('');
    this.confirmPassword.set('');
  }
}
