/**
 * One line of a function directory listing.
 *
 * `size` is carried by files and `entries` by directories, so each is optional:
 * the server leaves out the one that means nothing for that kind of entry.
 * `entries` counts one level, never a walk.
 */
export interface FunctionFileEntry {
  name: string;
  dir: boolean;
  size?: number;
  entries?: number;
  modified: string;
}

/** One level of a function directory. `path` is relative to the function root. */
export interface FunctionFileListing {
  path: string;
  entries: FunctionFileEntry[];
}

/** A file rendered inline: textual, and under the server's view cap. */
export interface FunctionFileContent {
  path: string;
  size: number;
  content: string;
}

/**
 * Why a file was not rendered inline. The server answers `binary` on a NUL byte
 * in the first kilobytes, and `too large` with the size past the view cap. The
 * download is offered in both cases — the cap is on what is displayed, not on
 * what can be fetched.
 */
export interface FunctionFileRefusal {
  error: 'binary' | 'too large' | string;
  size?: number;
}
