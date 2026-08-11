import { Component, signal, inject } from '@angular/core';
import { ActivatedRoute, Router } from '@angular/router';
import { FormsModule } from '@angular/forms';
import { AuthService } from '@/auth/auth.service';
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
    <div class="flex min-h-screen items-center justify-center p-4">
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
                [disabled]="loading()"
              />
            </z-form-control>
          </z-form-field>

          <button z-button type="submit" [zLoading]="loading()" [zDisabled]="!email || !password || loading()">
            Sign in
          </button>
        </form>
      </z-card>
    </div>
  `,
})
export class LoginComponent {
  private readonly authService = inject(AuthService);
  private readonly router = inject(Router);
  private readonly route = inject(ActivatedRoute);

  email = '';
  password = '';
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
