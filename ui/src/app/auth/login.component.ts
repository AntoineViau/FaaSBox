import { Component, signal, inject } from '@angular/core';
import { ActivatedRoute, Router } from '@angular/router';
import { FormControl, FormsModule, Validators } from '@angular/forms';
import { safeReturnUrl } from '@/auth/auth.guard';
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
    <!-- The title carries the name of the instance the way the editor header
         does, so the box you are signing in to is named before you sign in and
         not after. It is a template rather than a string because the name is
         set apart in muted grey, and z-card takes either. -->
    <ng-template #cardTitle>
      FaaSBox
      @if (instanceName()) {
        <span class="font-normal text-muted-foreground">{{ instanceName() }}</span>
      }
    </ng-template>

    <div class="flex min-h-full items-center justify-center p-4">
      <z-card
        [zTitle]="cardTitle"
        zDescription="Sign in with your superuser account"
        class="w-full max-w-sm"
      >
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
   * same session as anywhere else.
   *
   * Elsewhere, a link to this page may name the account in `?email=`, and only
   * the password is left to type. The showcase wins over the link: its fields
   * are read-only, and any other address would only fail. The password is never
   * read from the address bar — it would end up in the history and the logs.
   *
   * Read once, in a field initializer: the mode is loaded before the first
   * render and the link is already in the address bar, so there is nothing to
   * wait for and nothing to overwrite once the visitor starts typing.
   */
  email = this.instance.demoMode() ? this.instance.demoEmail() : this.emailFromUrl();
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
   * What this box is called, empty on an instance that sets no name. Same
   * source and same timing as the editor header: read before the first render,
   * so the card never shows one wording and then another.
   */
  protected readonly instanceName = this.instance.name;

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
      this.router.navigateByUrl(
        safeReturnUrl(this.route.snapshot.queryParamMap.get('returnUrl')),
      );
    } catch (error) {
      this.errorMessage.set(
        error instanceof Error ? error.message : 'Authentication failed',
      );
    } finally {
      this.loading.set(false);
    }
  }

  /**
   * The address a link to this page carries in `?email=`, when it is one.
   *
   * Anything else is dropped without a word and without being tidied up: a
   * prefill is a convenience, and an empty field says nothing wrong. Validity is
   * Angular's own email rule, which holds an empty value for valid, hence the
   * first test.
   *
   * A `+` has to reach the page as `%2B`: the router reads a bare one as a
   * space, like any query string, and `a+tag@example.com` would then fail.
   */
  private emailFromUrl(): string {
    const raw = this.route.snapshot.queryParamMap.get('email') ?? '';
    return raw && Validators.email(new FormControl(raw)) === null ? raw : '';
  }
}
