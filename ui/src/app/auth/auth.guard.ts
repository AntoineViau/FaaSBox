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
