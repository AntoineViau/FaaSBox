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
      // Both statuses mean the same thing here: the session is dead. 401 is an
      // outright rejection; 403 is what PocketBase answers when a superuser-only
      // rule is hit without usable credentials, and that covers two cases the
      // client cannot tell apart.
      //
      // The first is an expiry detected locally: getToken() dropped the expired
      // token, so the request went out bare. The second is a token the server no
      // longer recognises — a restarted instance whose superuser record was
      // rebuilt, a restored database. There the JWT still parses and its exp is
      // still in the future, so isAuthenticated() says yes and nothing on this
      // side can know better. Keying on it left the user in a working-looking
      // editor that answered 403 to everything and never sent them back.
      //
      // No 403 reaches this app on a live session: every route the SPA calls is
      // superuser-only, and the scope refusals that answer 403 belong to the
      // API-key routes, which a browser navigation never carries a key for.
      if (error.status === 401 || error.status === 403) {
        authService.logout();
      }
      return throwError(() => error);
    }),
  );
};
