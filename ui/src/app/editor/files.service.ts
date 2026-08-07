import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

import type {
  FunctionFileContent,
  FunctionFileListing,
} from '@/models/function-file.model';

const BASE_URL = '/api/faasbox/functions';

/**
 * Reads what a function's directory holds on disk. Superuser only, and
 * read-only by design: the tab consults, it never writes.
 *
 * These two routes are the only ones that show what the disk carries rather
 * than what the database does — what `bun install` left behind, and whatever a
 * running function wrote and never cleaned up.
 *
 * The path segment is the function id. The routes take a name just as well, but
 * the directory itself is named by the id, and asking by id is the spelling that
 * cannot be invalidated by a rename in flight.
 */
@Injectable({ providedIn: 'root' })
export class FilesService {
  private readonly http = inject(HttpClient);

  /** One level of the directory. Never recursive; an absent directory lists empty. */
  list(functionId: string, path: string) {
    return this.http.get<FunctionFileListing>(`${BASE_URL}/${functionId}/files`, {
      params: { path },
    });
  }

  /**
   * A file rendered inline. Fails with a body saying why when the file is
   * binary or past the view cap — read that body rather than the status text,
   * it is what names the reason.
   */
  read(functionId: string, path: string) {
    return this.http.get<FunctionFileContent>(`${BASE_URL}/${functionId}/files/content`, {
      params: { path },
    });
  }

  /**
   * The file itself, uncapped. It comes back through HttpClient rather than a
   * plain link because the route is superuser-only: a browser navigation
   * carries no Authorization header and would come back 401.
   */
  download(functionId: string, path: string) {
    return this.http.get(`${BASE_URL}/${functionId}/files/content`, {
      params: { path, download: '1' },
      responseType: 'blob',
    });
  }
}
