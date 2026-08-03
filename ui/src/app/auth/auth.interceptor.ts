import { HttpInterceptorFn, HttpErrorResponse } from '@angular/common/http';
import { inject } from '@angular/core';
import { catchError, throwError } from 'rxjs';
import { AuthService } from '@/auth/auth.service';

export const authInterceptor: HttpInterceptorFn = (req, next) => {
  const authService = inject(AuthService);
  const token = authService.getToken();

  if (token) {
    req = req.clone({
      headers: req.headers.set('Authorization', token),
    });
  }

  return next(req).pipe(
    catchError((error: HttpErrorResponse) => {
      // 401 means the server rejected the token. 403 is what PocketBase answers
      // when a superuser-only rule is hit without any credentials at all, which
      // is exactly what happens once getToken() has dropped an expired token:
      // the request goes out unauthenticated. Only treat it as a dead session
      // when the token is indeed gone, so a genuine authorization refusal on a
      // live session still reaches the caller.
      if (error.status === 401 || (error.status === 403 && !authService.isAuthenticated())) {
        authService.logout();
      }
      return throwError(() => error);
    }),
  );
};
