package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

const (
	// defaultMaxFileView bounds what is rendered inline, not what can be
	// downloaded: showing 40 MB in a browser is a defect, fetching them is not.
	defaultMaxFileView = 256 << 10 // 256 KB
	// binarySniffSize is how much of a file is read looking for a NUL byte.
	binarySniffSize = 8 << 10 // 8 KB
)

// maxFileView is the number of bytes a file may hold and still be rendered
// inline. Configurable via FAASBOX_MAX_FILE_VIEW environment variable.
var maxFileView = envInt("FAASBOX_MAX_FILE_VIEW", defaultMaxFileView)

// errOutsideFunction is returned when a requested path lands outside the
// function directory, whichever way it got there. It is a rule, not an absence:
// callers answer 400, never 404.
var errOutsideFunction = errors.New("path escapes the function directory")

// fileEntry is one line of a directory listing. Size is carried by files and
// Entries by directories, so each is a pointer: a pointer keeps a zero-byte file
// reporting "size": 0 while leaving the field out where it means nothing.
type fileEntry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     *int64 `json:"size,omitempty"`
	Entries  *int   `json:"entries,omitempty"`
	Modified string `json:"modified"`
}

// resolveFunctionPath returns the absolute path rel designates inside the
// directory of the named function, or an error when it lands outside.
//
// EvalSymlinks is not a matter of form. Functions execute arbitrary code with
// their own directory in reach, so one can drop a link to /etc/passwd or to
// pb_data and wait for it to be followed. A Clean would stop a "../.." written
// in the URL, never a link planted at run time.
//
// The order is what makes it hold: join, resolve, *then* check the result is
// prefixed by the resolved root. Checking before resolving lets the link
// through. The root is resolved too — the functions directory may itself sit
// behind a link, and comparing a resolved target to an unresolved root would
// reject everything.
//
// The lexical check before the resolution refuses rather than accepts, so it
// weakens nothing: it is there so that "../../pb_data/data.db" is answered as
// the refusal it is, instead of turning into a 404 the day the file happens not
// to exist. Collapsing the "../" against the root would have hidden the attempt
// altogether.
//
// A path that does not exist surfaces as fs.ErrNotExist so callers can tell a
// missing entry from a refusal.
func resolveFunctionPath(functionsDir, name, rel string) (string, error) {
	if !validName.MatchString(name) || len(name) > 64 {
		return "", fmt.Errorf("invalid function name: %s", name)
	}

	absDir, err := filepath.Abs(filepath.Join(functionsDir, name))
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return "", err
	}

	target := filepath.Join(root, rel)
	if !withinRoot(root, target) {
		return "", errOutsideFunction
	}

	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if !withinRoot(root, resolved) {
		return "", errOutsideFunction
	}
	return resolved, nil
}

// withinRoot reports whether path is root itself or sits under it. The trailing
// separator is what keeps "/functions/app-extra" from passing for a child of
// "/functions/app".
func withinRoot(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// functionFilesHandler answers GET /api/faasbox/functions/{name}/files with one
// level of the function directory. Superuser only.
//
// A function that was never invoked and declares no dependencies may have no
// directory on disk yet. That is not an error: the root listing comes back
// empty, which is the truth, and the tab says so.
func functionFilesHandler(e *core.RequestEvent, functionsDir string) error {
	name := e.Request.PathValue("name")
	if !validName.MatchString(name) || len(name) > 64 {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid function name"})
	}

	rel := e.Request.URL.Query().Get("path")
	dir, err := resolveFunctionPath(functionsDir, name, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if rel == "" {
				return e.JSON(http.StatusOK, map[string]any{"path": "", "entries": []fileEntry{}})
			}
			return e.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return e.JSON(http.StatusBadRequest, map[string]string{"error": pathErrorMessage(err)})
	}

	info, err := os.Stat(dir)
	if err != nil {
		return e.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if !info.IsDir() {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "not a directory"})
	}

	entries, err := readLevel(dir)
	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "cannot read the directory",
		})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"path":    normalizeRel(rel),
		"entries": entries,
	})
}

// readLevel lists one directory level — never a walk. Directories come first,
// then files, each group sorted by name, so the tab renders what it receives.
//
// A directory entry carries the number of entries of its own level, which costs
// one extra ReadDir per directory. Counting recursively to show "41,812 files"
// is what must not be done: the cost stops being negligible and the number
// answers no question.
func readLevel(dir string) ([]fileEntry, error) {
	raw, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	entries := make([]fileEntry, 0, len(raw))
	for _, item := range raw {
		info, err := item.Info()
		if err != nil {
			continue // vanished between the read and the stat
		}

		entry := fileEntry{
			Name:     item.Name(),
			Dir:      item.IsDir(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		}
		if item.IsDir() {
			if children, err := os.ReadDir(filepath.Join(dir, item.Name())); err == nil {
				count := len(children)
				entry.Entries = &count
			}
		} else {
			size := info.Size()
			entry.Size = &size
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// functionFileContentHandler answers GET /api/faasbox/functions/{name}/files/content.
// Superuser only.
//
// Two modes on the same route. Without ?download=1 the file is rendered inline
// when it is textual and under maxFileView; otherwise the response says why and
// the client offers the download alone. With the flag, http.ServeContent hands
// the file back as an attachment, uncapped.
func functionFileContentHandler(e *core.RequestEvent, functionsDir string) error {
	name := e.Request.PathValue("name")
	if !validName.MatchString(name) || len(name) > 64 {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid function name"})
	}

	rel := e.Request.URL.Query().Get("path")
	if rel == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "missing path"})
	}

	path, err := resolveFunctionPath(functionsDir, name, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return e.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return e.JSON(http.StatusBadRequest, map[string]string{"error": pathErrorMessage(err)})
	}

	info, err := os.Stat(path)
	if err != nil {
		return e.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if info.IsDir() {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "directory"})
	}

	file, err := os.Open(path)
	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": "cannot read the file"})
	}
	defer file.Close()

	if e.Request.URL.Query().Get("download") == "1" {
		e.Response.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
		http.ServeContent(e.Response, e.Request, filepath.Base(path), info.ModTime(), file)
		return nil
	}

	// Sniffed before the size is weighed: an oversized binary is refused for
	// being binary, which is the reason that stays true whatever the cap is.
	head := make([]byte, binarySniffSize)
	// ReadFull rather than Read: a single Read is allowed to come back short on
	// a perfectly healthy file, and a NUL sitting past that point would be missed.
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": "cannot read the file"})
	}
	// A NUL byte, never the extension: node_modules is full of files without one.
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return e.JSON(http.StatusUnsupportedMediaType, map[string]any{"error": "binary"})
	}

	if info.Size() > int64(maxFileView) {
		return e.JSON(http.StatusRequestEntityTooLarge, map[string]any{
			"error": "too large",
			"size":  info.Size(),
		})
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": "cannot read the file"})
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": "cannot read the file"})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"path":    normalizeRel(rel),
		"size":    info.Size(),
		"content": string(content),
	})
}

// pathErrorMessage keeps the refusal legible without publishing an absolute
// server path: an escape is named, anything else stays generic.
func pathErrorMessage(err error) string {
	if errors.Is(err, errOutsideFunction) {
		return "path escapes the function directory"
	}
	return "invalid path"
}

// normalizeRel renders back the path the server actually walked, so the client
// never has to guess what "" or "./a/../b" resolved to.
func normalizeRel(rel string) string {
	cleaned := strings.TrimPrefix(filepath.Clean("/"+rel), "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}
