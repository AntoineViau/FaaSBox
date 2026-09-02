import { Component, signal, inject } from '@angular/core';
import { ActivatedRoute, Router } from '@angular/router';
import { FormsModule } from '@angular/forms';
import { AuthService } from '@/auth/auth.service';
import { InstanceService } from '@/instance/instance.service';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardInputDirective } from '@shared/components/input';
import { ZardCardComponent } from '@shared/components/card';
import {
  ZardFormFieldComponent,
  ZardFormLabelComponent,
  ZardFormControlComponent,
} from '@shared/components/form';
import { ZardAlertComponent } from '@shared/components/alert';

@Component({
  selector: 'app-login',
  standalone: true,
  imports: [
    FormsModule,
    ZardButtonComponent,
    ZardInputDirective,
    ZardCardComponent,
    ZardFormFieldComponent,
    ZardFormLabelComponent,
    ZardFormControlComponent,
    ZardAlertComponent,
  ],
  template: `
    <div class="flex min-h-full items-center justify-center p-4">
      <z-card zTitle="FaaSBox" zDescription="Sign in with your superuser account" class="w-full max-w-sm">
        <form (ngSubmit)="onSubmit()" class="flex flex-col gap-4">
          @if (errorMessage()) {
            <z-alert zType="destructive" zTitle="Error" [zDescription]="errorMessage()" />
          }

          <z-form-field>
            <z-form-label zRequired>Email</z-form-label>
            <z-form-control>
              <input
                z-input
                type="email"
                placeholder="admin@example.com"
                [(ngModel)]="email"
                name="email"
                required
                [readonly]="demoMode()"
                [disabled]="loading()"
              />
            </z-form-control>
          </z-form-field>

          <z-form-field>
            <z-form-label zRequired>Password</z-form-label>
            <z-form-control>
              <input
                z-input
                type="password"
                placeholder="Password"
                [(ngModel)]="password"
                name="password"
                required
                [readonly]="demoMode()"
                [disabled]="loading()"
              />
            </z-form-control>
          </z-form-field>

          <button
            z-button
            type="submit"
            [class]="demoMode() ? DEMO_BUTTON_CLASSES : ''"
            [zLoading]="loading()"
            [zDisabled]="!email || !password || loading()"
          >
            {{ demoMode() ? 'Click here to sign in to the demo' : 'Sign in' }}
          </button>
        </form>
      </z-card>
    </div>
  `,
})
export class LoginComponent {
  private readonly authService = inject(AuthService);
  private readonly instance = inject(InstanceService);
  private readonly router = inject(Router);
  private readonly route = inject(ActivatedRoute);

  /**
   * A showcase hands the visitor the account it wants them to use, already
   * typed in. Nothing else about signing in changes: the click below opens the
   * same session as anywhere else, and the two fields are empty on an instance
   * that publishes no demo credentials.
   *
   * Read once, in a field initializer: the mode is loaded before the first
   * render, so there is nothing to wait for and nothing to overwrite once the
   * visitor starts typing.
   */
  email = this.instance.demoMode() ? this.instance.demoEmail() : '';
  password = this.instance.demoMode() ? this.instance.demoPassword() : '';

  /**
   * The button says what the filled-in fields already imply: on a showcase, the
   * click is the whole sign-in, and nothing has to be typed first. The two
   * fields are `readonly` for the same reason — the credentials are the ones the
   * showcase publishes, and any other pair would only fail.
   *
   * `readonly` and not `disabled`: a disabled field is skipped by the tab order
   * and greyed out, which would read as broken, and its value would no longer be
   * submitted. A read-only field is still focusable, selectable and copyable.
   */
  protected readonly demoMode = this.instance.demoMode;

  /**
   * The submit button wears the banner's yellow in demo mode: the two are the
   * only things on the page that belong to the showcase rather than to FaaSBox,
   * and sharing a colour is what says so. The fill is fixed in both themes, like
   * the banner's, so the pair cannot drift apart.
   */
  protected readonly DEMO_BUTTON_CLASSES =
    'bg-yellow-400 text-yellow-950 hover:bg-yellow-300 dark:bg-yellow-500 dark:hover:bg-yellow-400';
  loading = signal(false);
  errorMessage = signal('');

  async onSubmit(): Promise<void> {
    if (!this.email || !this.password) return;

    this.loading.set(true);
    this.errorMessage.set('');

    try {
      await this.authService.login(this.email, this.password);
      this.router.navigateByUrl(this.returnUrl());
    } catch (error) {
      this.errorMessage.set(
        error instanceof Error ? error.message : 'Authentication failed',
      );
    } finally {
      this.loading.set(false);
    }
  }

  /**
   * Where to go once signed in: back to the page authGuard turned away, or the
   * editor.
   *
   * Only a path of this application is followed. A value starting with `//`, or
   * carrying a scheme, would turn the login page into an open redirect — and the
   * parameter is in the address bar, so it is whatever anyone cares to put there.
   */
  private returnUrl(): string {
    const raw = this.route.snapshot.queryParamMap.get('returnUrl') ?? '';
    return raw.startsWith('/') && !raw.startsWith('//') ? raw : '/editor';
  }
}
