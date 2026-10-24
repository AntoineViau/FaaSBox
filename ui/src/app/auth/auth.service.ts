import { Injectable, signal, computed, inject, untracked } from '@angular/core';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Router } from '@angular/router';
import { firstValueFrom } from 'rxjs';

interface AuthResponse {
  token: string;
  record: {
    id: string;
    email: string;
  };
}

/** What the account dialog submits; an unchanged field is left out of the PATCH. */
export interface AccountChanges {
  email: string;
  /** Empty keeps the current one. */
  password: string;
}

/** The claims of a PocketBase auth token this service reads. */
interface TokenClaims {
  id: string;
  exp: number;
}

const TOKEN_KEY = 'faasbox_token';

/**
 * The window of PocketBase's `*:auth` rate limit rule — 2 attempts per 3 s, its
 * default in `core/settings_model.go`. The counter starts over once that long
 * has passed since the last accepted attempt, and a refusal does not extend it,
 * so waiting one window after a `429` is enough. A window lengthened by hand in
 * the admin is not read here: the retry then fails, and the fallback applies.
 */
const AUTH_RATE_WINDOW_MS = 3000;

@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly router = inject(Router);
  private readonly http = inject(HttpClient);
  private readonly token = signal<string | null>(this.loadToken());

  readonly isAuthenticated = computed(() => {
    const t = this.token();
    return t !== null && !this.isTokenExpired(t);
  });

  /**
   * The token to send with a request, read without tracking. The interceptor
   * calls this from whatever reactive context an effect opened its request in,
   * and a tracked read made every such effect depend on the token: an account
   * change, which swaps it in place, reran them all — every panel reloaded over
   * its unsaved edits, and the log stream reopened.
   */
  getToken(): string | null {
    return untracked(() => {
      const t = this.token();
      if (t && this.isTokenExpired(t)) {
        this.clearToken();
        return null;
      }
      return t;
    });
  }

  async login(email: string, password: string): Promise<void> {
    const response = await fetch('/api/collections/_superusers/auth-with-password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ identity: email, password }),
    });

    if (!response.ok) {
      const error = await response.json().catch(() => ({}));
      throw new Error(error.message || 'Authentication failed');
    }

    const data: AuthResponse = await response.json();
    this.token.set(data.token);
    localStorage.setItem(TOKEN_KEY, data.token);
  }

  /** The superuser's address, which the token does not carry. */
  async accountEmail(): Promise<string> {
    const { email } = await firstValueFrom(this.http.get<{ email: string }>(this.recordUrl()));
    return email;
  }

  /**
   * Changes the superuser's address, password, or both, and keeps this tab
   * signed in.
   *
   * The current password is checked by signing in with it, not through
   * `oldPassword`: PocketBase only demands that field from a caller without
   * manage access, and a superuser has it. The check guards against a typo and
   * a session left open on a shared screen — not against a stolen token, which
   * can send the `PATCH` itself.
   *
   * The first three calls go through `HttpClient`, so they carry the session's
   * token: a superuser request skips the rate limiter, and a mistyped current
   * password costs nothing under `*:auth`. Only the sign-in that follows the
   * `PATCH` counts, since it leaves with a dead token.
   */
  async updateAccount(currentPassword: string, changes: AccountChanges): Promise<void> {
    const record = this.recordUrl();
    // Read again rather than trusted from the dialog: it may have changed in /_/ since.
    const email = await this.accountEmail();

    // Exact comparison: an address in another case is a change, which PocketBase
    // accepts. Its format is PocketBase's to judge, not copied here.
    const body: Record<string, string> = {};
    if (changes.email !== email) body['email'] = changes.email;
    if (changes.password) {
      body['password'] = changes.password;
      body['passwordConfirm'] = changes.password;
    }
    if (Object.keys(body).length === 0) return;

    try {
      await firstValueFrom(
        this.http.post('/api/collections/_superusers/auth-with-password', {
          identity: email,
          password: currentPassword,
        }),
      );
    } catch (error) {
      if (error instanceof HttpErrorResponse && error.status === 400) {
        throw new Error('Current password is incorrect.');
      }
      throw error;
    }

    try {
      await firstValueFrom(this.http.patch(record, body));
    } catch (error) {
      throw fieldRefusal(error) ?? error;
    }

    // The PATCH regenerated the account's tokenKey, whichever field changed:
    // the token this tab holds is dead. If no new one can be had, the account
    // is changed all the same, and the user signs in again with what they just
    // typed.
    if (!(await this.signInAgain(changes.email, changes.password || currentPassword))) {
      this.logout();
    }
  }

  logout(): void {
    this.clearToken();
    this.router.navigate(['/login']);
  }

  /**
   * One retry, one window later, whatever the failure: `login()` does not
   * surface the status, and after a successful `PATCH` a second attempt costs
   * nothing. A `429` here means other sign-ins drained `*:auth` for this address
   * — behind a reverse proxy, every client shares the proxy's.
   */
  private async signInAgain(email: string, password: string): Promise<boolean> {
    for (let attempt = 0; attempt < 2; attempt++) {
      if (attempt > 0) {
        await new Promise((resolve) => setTimeout(resolve, AUTH_RATE_WINDOW_MS));
      }
      try {
        await this.login(email, password);
        return true;
      } catch {
        // Retried once, then reported to the caller.
      }
    }
    return false;
  }

  /** The superuser's own record, named by the `id` claim of the token. */
  private recordUrl(): string {
    const t = this.token();
    const id = t ? this.claims(t)?.id : undefined;
    if (!id) throw new Error('Not signed in.');
    return `/api/collections/_superusers/records/${id}`;
  }

  private clearToken(): void {
    this.token.set(null);
    localStorage.removeItem(TOKEN_KEY);
  }

  private loadToken(): string | null {
    const token = localStorage.getItem(TOKEN_KEY);
    if (token && this.isTokenExpired(token)) {
      localStorage.removeItem(TOKEN_KEY);
      return null;
    }
    return token;
  }

  private isTokenExpired(token: string): boolean {
    const claims = this.claims(token);
    return claims === null || claims.exp * 1000 < Date.now();
  }

  /** The payload of a token, or null when it does not parse. */
  private claims(token: string): TokenClaims | null {
    try {
      return JSON.parse(atob(token.split('.')[1]));
    } catch {
      return null;
    }
  }
}

/** The fields a refused `PATCH` can name, with the label the dialog gives each. */
const REFUSED_FIELDS = [
  ['email', 'Email'],
  ['password', 'New password'],
] as const;

/**
 * Why PocketBase refused the change — an invalid or taken address, a password
 * too short. The reason sits under `data.<field>`, and does not name the field;
 * the top-level message only says the record could not be updated.
 */
function fieldRefusal(error: unknown): Error | null {
  if (!(error instanceof HttpErrorResponse)) return null;
  for (const [field, label] of REFUSED_FIELDS) {
    const message = error.error?.data?.[field]?.message;
    if (typeof message === 'string' && message) return new Error(`${label}: ${message}`);
  }
  return null;
}
