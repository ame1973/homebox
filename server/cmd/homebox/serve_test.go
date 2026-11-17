package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homebox/internal/store"
)

type startupOutput chan []byte

func (out startupOutput) Write(data []byte) (int, error) {
	out <- append([]byte(nil), data...)
	return len(data), nil
}

func serverFixture(t *testing.T) (database, web string) {
	t.Helper()
	directory := privateDirectory(t)
	database = filepath.Join(directory, "homebox.sqlite")
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
	}
	web = filepath.Join(directory, "web")
	if err := os.Mkdir(web, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>Test fixture</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return database, web
}

func TestServeActualSocketAndGracefulShutdownReleaseDatabase(t *testing.T) {
	database, web := serverFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := make(startupOutput, 1)
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, []string{"serve", "--listen", "127.0.0.1:0", "--database", database, "--web-dir", web}, output, io.Discard)
	}()
	var ready map[string]string
	select {
	case data := <-output:
		if err := json.Unmarshal(data, &ready); err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatalf("server exited before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + ready["listening"] + "/api/v1/health/live")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]string
	err = json.NewDecoder(response.Body).Decode(&health)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || health["status"] != "ok" {
		t.Fatalf("real HTTP liveness failed: %v, %v", health, err)
	}
	if _, err = store.Open(t.Context(), store.Config{Path: database}); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("server did not retain database ownership: %v", err)
	}
	cancel()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if _, err = invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatalf("shutdown leaked database ownership: %v", err)
	}
}

func requirePrivateWebStartupRejected(t *testing.T, database, web, exposed string, extra ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := make(startupOutput, 1)
	result := make(chan error, 1)
	args := append([]string{"--listen", "127.0.0.1:0", "--database", database, "--web-dir", web}, extra...)
	go func() { result <- serve(ctx, args, output, io.Discard) }()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "private") {
			t.Fatalf("expected a private-path startup rejection, got %v", err)
		}
	case data := <-output:
		var ready map[string]string
		if err := json.Unmarshal(data, &ready); err != nil {
			t.Fatal(err)
		}
		leaked := false
		if exposed != "" {
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get("http://" + ready["listening"] + exposed)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 32))
				_ = response.Body.Close()
				leaked = readErr == nil && response.StatusCode == http.StatusOK && strings.HasPrefix(string(body), "SQLite format 3\x00")
			}
		}
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("unsafe server did not stop cleanly: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("unsafe server did not stop")
		}
		t.Fatalf("server started with private data in its web scope; SQLite bytes exposed: %t", leaked)
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not reject or finish")
	}
}

func TestServeRejectsPrivateDatabaseAndMediaWebOverlap(t *testing.T) {
	database, web := serverFixture(t)
	directory := filepath.Dir(database)
	privateDB := filepath.Join(web, "private.png")
	nested := filepath.Join(web, "private")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedDB := filepath.Join(nested, "nested.png")
	for _, path := range []string{privateDB, nestedDB} {
		if _, err := invoke(t, "db", "migrate", "--database", path); err != nil {
			t.Fatal(err)
		}
	}
	webAlias := filepath.Join(directory, "web-alias")
	databaseAlias := filepath.Join(directory, "database-alias")
	for _, alias := range []string{webAlias, databaseAlias} {
		if err := os.Symlink(web, alias); err != nil {
			t.Fatal(err)
		}
	}
	mediaWeb := filepath.Join(database+".media", "public")
	backupWeb := filepath.Join(directory, "migration-backups", "public")
	for _, path := range []string{mediaWeb, backupWeb} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "index.html"), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ name, db, assets, exposed string }{
		{"database in web", privateDB, web, "/private.png"},
		{"database below web", nestedDB, web, "/private/nested.png"},
		{"web root symlink", privateDB, webAlias, "/private.png"},
		{"database parent symlink", filepath.Join(databaseAlias, "private.png"), web, "/private.png"},
		{"web inside media", database, mediaWeb, ""},
		{"web inside default migration backups", database, backupWeb, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			requirePrivateWebStartupRejected(t, test.db, test.assets, test.exposed)
		})
	}
}

func TestServeRejectsPrivateHardLinksIntoWeb(t *testing.T) {
	t.Run("database", func(t *testing.T) {
		database, web := serverFixture(t)
		if err := os.Link(database, filepath.Join(web, "database.png")); err != nil {
			t.Fatal(err)
		}
		requirePrivateWebStartupRejected(t, database, web, "/database.png")
	})
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".lock"} {
		t.Run("database sidecar "+suffix, func(t *testing.T) {
			database, web := serverFixture(t)
			sidecar := database + suffix
			if _, err := os.Lstat(sidecar); errors.Is(err, os.ErrNotExist) {
				if err = os.WriteFile(sidecar, []byte("private sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := os.Link(sidecar, filepath.Join(web, "sidecar.png")); err != nil {
				t.Fatal(err)
			}
			requirePrivateWebStartupRejected(t, database, web, "")
			if _, err := os.Lstat(sidecar); err != nil {
				t.Fatal("rejected startup removed a private sidecar", err)
			}
		})
	}
	t.Run("TLS key", func(t *testing.T) {
		database, web := serverFixture(t)
		cert, key, _ := testCertificateFiles(t, filepath.Dir(database))
		if err := os.Link(key, filepath.Join(web, "key.png")); err != nil {
			t.Fatal(err)
		}
		requirePrivateWebStartupRejected(t, database, web, "", "--tls-cert", cert, "--tls-key", key)
	})
}

func TestServeRejectsPrivateBackupAndTLSKeyWebAliases(t *testing.T) {
	t.Run("media root symlink", func(t *testing.T) {
		database, web := serverFixture(t)
		if err := os.Symlink(web, database+".media"); err != nil {
			t.Fatal(err)
		}
		requirePrivateWebStartupRejected(t, database, web, "")
	})
	t.Run("default backup symlink", func(t *testing.T) {
		database, web := serverFixture(t)
		if err := os.Symlink(web, filepath.Join(filepath.Dir(database), "migration-backups")); err != nil {
			t.Fatal(err)
		}
		requirePrivateWebStartupRejected(t, database, web, "")
	})
	t.Run("TLS key inside web", func(t *testing.T) {
		database, web := serverFixture(t)
		cert, key, _ := testCertificateFiles(t, web)
		requirePrivateWebStartupRejected(t, database, web, "", "--tls-cert", cert, "--tls-key", key)
	})
	t.Run("TLS key path inside web", func(t *testing.T) {
		database, web := serverFixture(t)
		cert, key, _ := testCertificateFiles(t, filepath.Dir(database))
		alias := filepath.Join(web, "key-alias.pem")
		if err := os.Symlink(key, alias); err != nil {
			t.Fatal(err)
		}
		requirePrivateWebStartupRejected(t, database, web, "", "--tls-cert", cert, "--tls-key", alias)
	})
	t.Run("TLS key resolves inside web", func(t *testing.T) {
		database, web := serverFixture(t)
		cert, key, _ := testCertificateFiles(t, web)
		alias := filepath.Join(filepath.Dir(database), "key-alias.pem")
		if err := os.Symlink(key, alias); err != nil {
			t.Fatal(err)
		}
		requirePrivateWebStartupRejected(t, database, web, "", "--tls-cert", cert, "--tls-key", alias)
	})
}

func TestServeAllowsSeparatedSiblingPathsWithSimilarNames(t *testing.T) {
	database, web := serverFixture(t)
	parent := filepath.Join(filepath.Dir(database), "web-private")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	database = filepath.Join(parent, "homebox.sqlite")
	if _, err := invoke(t, "db", "migrate", "--database", database); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(parent), "safe-web-alias")
	databaseAlias := filepath.Join(filepath.Dir(parent), "safe-database-alias")
	if err := os.Symlink(web, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, databaseAlias); err != nil {
		t.Fatal(err)
	}
	database = filepath.Join(databaseAlias, "homebox.sqlite")
	ready, stop := startServe(t, "--listen", "127.0.0.1:0", "--database", database, "--web-dir", alias)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + ready["listening"] + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Test fixture") {
		t.Fatal("safe sibling paths did not serve the frontend", response.StatusCode, err)
	}
	stop()
}

func TestServeRejectsChangedFrontendRootAndDanglingPrivatePaths(t *testing.T) {
	database, web := serverFixture(t)
	root, err := os.OpenRoot(web)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	if err = os.Rename(web, web+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(web, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = requirePrivateWebSeparation(root, web, database, ""); !errors.Is(err, errPrivateWebOverlap) {
		t.Fatal("replaced frontend root was accepted", err)
	}
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	root, err = os.OpenRoot(web)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(web, "missing"), database+".media"); err != nil {
		t.Fatal(err)
	}
	if err = requirePrivateWebSeparation(root, web, database, ""); err == nil {
		t.Fatal("a dangling private path was accepted")
	}
}

func TestServeRejectsMissingBuildInvalidFlagsAndUnmigratedData(t *testing.T) {
	database, web := serverFixture(t)
	for _, args := range [][]string{
		{"--unknown"}, {"extra"}, {"--web-dir", filepath.Join(web, "missing")},
		{"--web-dir", web, "--database", filepath.Join(filepath.Dir(database), "missing.sqlite")},
		{"--web-dir", web, "--database", database, "--listen", "invalid-address"},
	} {
		if err := serve(t.Context(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid serve arguments accepted: %v", args)
		}
	}
	if err := serve(t.Context(), []string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := serve(ctx, nil, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := serve(t.Context(), []string{"--web-dir", web, "--database", database, "--listen", "127.0.0.1:0"}, failedOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatalf("failed startup leaked database ownership: %v", err)
	}
}
