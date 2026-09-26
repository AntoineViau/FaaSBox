import { CanActivateFn, Router } from '@angular/router';
import { inject } from '@angular/core';
import { AuthService } from '@/auth/auth.service';

export const authGuard: CanActivateFn = (_route, state) => {
  const authService = inject(AuthService);
  const router = inject(Router);

  if (authService.isAuthenticated()) {
    return true;
  }

  // The address is carried to /login so the visitor comes back to it. It matters
  // on /consent above all: that page is reached by a redirection the user did not
  // type, so dropping them on the editor after signing in would strand the
  // authorization request they were in the middle of, with no way back to it.
  return router.createUrlTree(['/login'], { queryParams: { returnUrl: state.url } });
};

/**
 * Keeps a signed-in visitor off /login: a link to the sign-in page, one that
 * names the account in `?email=` included, takes them where they were going
 * instead of showing a form they have no use for.
 *
 * It cannot loop. The two ways onto /login both arrive without a session:
 * authGuard only sends there a visitor it turned away, and AuthService.logout()
 * drops the token before it navigates. A token the server no longer honours
 * makes one round trip — here, the editor, a 403, logout(), the form.
 */
export const guestGuard: CanActivateFn = (route) => {
  if (!inject(AuthService).isAuthenticated()) {
    return true;
  }
  return inject(Router).parseUrl(safeReturnUrl(route.queryParamMap.get('returnUrl')));
};

/**
 * Where to send a visitor once signed in: back to the page authGuard turned
 * away, or the editor.
 *
 * Only a path of this application is followed. A value starting with `//`, or
 * carrying a scheme, would turn the login page into an open redirect — and the
 * parameter is in the address bar, so it is whatever anyone cares to put there.
 */
export function safeReturnUrl(raw: string | null): string {
  const url = raw ?? '';
  return url.startsWith('/') && !url.startsWith('//') ? url : '/editor';
}
