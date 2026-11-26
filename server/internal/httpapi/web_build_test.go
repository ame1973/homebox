package httpapi

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

const testWebWorker = "/pwa-worker-0123456789abcdef01234567.js"

func webBuildFixture(version string) fstest.MapFS {
	return fstest.MapFS{
		"web-build.json":                       {Data: []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":%q}\n", testWebWorker, version))},
		"pwa-version.json":                     {Data: []byte(fmt.Sprintf("{\"worker\":%q}\n", testWebWorker))},
		strings.TrimPrefix(testWebWorker, "/"): {Data: []byte("worker fixture")},
	}
}

func TestReadWebBuildAcceptsCanonicalPairedMetadata(t *testing.T) {
	for _, version := range []string{"0.1.0-dev", "1", "v1.2+build-3", strings.Repeat("x", 32)} {
		t.Run(version, func(t *testing.T) {
			got, err := readWebBuild(webBuildFixture(version))
			if err != nil || got.worker != testWebWorker || got.version != version {
				t.Fatalf("unexpected build metadata: %+v, %v", got, err)
			}
		})
	}
}

func TestReadWebBuildRejectsMissingMalformedOrMismatchedFiles(t *testing.T) {
	tests := map[string]func(fstest.MapFS){
		"missing build":      func(files fstest.MapFS) { delete(files, "web-build.json") },
		"missing descriptor": func(files fstest.MapFS) { delete(files, "pwa-version.json") },
		"missing worker":     func(files fstest.MapFS) { delete(files, strings.TrimPrefix(testWebWorker, "/")) },
		"empty worker":       func(files fstest.MapFS) { files[strings.TrimPrefix(testWebWorker, "/")].Data = nil },
		"worker directory":   func(files fstest.MapFS) { files[strings.TrimPrefix(testWebWorker, "/")].Mode = fs.ModeDir },
		"empty version": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":\"\"}\n", testWebWorker))
		},
		"long version": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":%q}\n", testWebWorker, strings.Repeat("x", 33)))
		},
		"trailing newline": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":%q}\n", testWebWorker, "1\n"))
		},
		"bad version": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":\"1..0\"}\n", testWebWorker))
		},
		"duplicate property": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":\"1\",\"web_version\":\"2\"}\n", testWebWorker))
		},
		"extra property": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"web_version\":\"1\",\"extra\":true}\n", testWebWorker))
		},
		"bad worker": func(files fstest.MapFS) {
			files["web-build.json"].Data = []byte("{\"worker\":\"/pwa-worker-../../secret.js\",\"web_version\":\"1\"}\n")
		},
		"mismatched worker": func(files fstest.MapFS) {
			files["pwa-version.json"].Data = []byte("{\"worker\":\"/pwa-worker-ffffffffffffffffffffffff.js\"}\n")
		},
		"descriptor extra": func(files fstest.MapFS) {
			files["pwa-version.json"].Data = []byte(fmt.Sprintf("{\"worker\":%q,\"other\":1}\n", testWebWorker))
		},
		"oversized build": func(files fstest.MapFS) { files["web-build.json"].Data = []byte(strings.Repeat("x", 257)) },
		"oversized descriptor": func(files fstest.MapFS) {
			files["pwa-version.json"].Data = []byte(strings.Repeat("x", 257))
		},
		"build directory": func(files fstest.MapFS) { files["web-build.json"].Mode = fs.ModeDir },
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			files := webBuildFixture("0.1.0-dev")
			alter(files)
			got, err := readWebBuild(files)
			if err == nil || got != (webBuildInfo{}) {
				t.Fatalf("invalid files returned partial metadata: %+v, %v", got, err)
			}
		})
	}
	if got, err := readWebBuild(nil); err == nil || got != (webBuildInfo{}) {
		t.Fatalf("nil filesystem returned partial metadata: %+v, %v", got, err)
	}
}

type faultyBuildFS struct {
	fs.FS
	target string
	read   func([]byte) (int, error)
	closed *bool
}

func (f faultyBuildFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.target {
		return file, err
	}
	return faultyBuildFile{File: file, read: f.read, closed: f.closed}, nil
}

type faultyBuildFile struct {
	fs.File
	read   func([]byte) (int, error)
	closed *bool
}

func (f faultyBuildFile) Read(value []byte) (int, error) { return f.read(value) }
func (f faultyBuildFile) Close() error {
	*f.closed = true
	return f.File.Close()
}

func TestReadWebBuildClosesAndRejectsFailedOrShortReads(t *testing.T) {
	for _, target := range []string{"web-build.json", "pwa-version.json"} {
		for name, read := range map[string]func([]byte) (int, error){
			"error": func([]byte) (int, error) { return 0, errors.New("private read error") },
			"short": func([]byte) (int, error) { return 0, io.EOF },
		} {
			t.Run(target+"/"+name, func(t *testing.T) {
				closed := false
				files := faultyBuildFS{FS: webBuildFixture("0.1.0-dev"), target: target, read: read, closed: &closed}
				got, err := readWebBuild(files)
				if err == nil || got != (webBuildInfo{}) || !closed {
					t.Fatalf("faulty read returned metadata or leaked a file: %+v, %v", got, err)
				}
			})
		}
	}
}
