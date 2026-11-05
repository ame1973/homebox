// Package httpapi serves the same-origin application and explicit API routes.
package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"strings"

	"homebox/internal/store"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' blob: data:; font-src 'self'; connect-src 'self'; media-src 'self' blob:; " +
	"worker-src 'self' blob:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

var hashedAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.(js|css|woff2|png|svg|webp|ico)$`)

// Config binds browser authentication to an operator-selected origin and socket peers.
// Assets must enforce containment; production passes os.Root.FS.
type Config struct {
	Assets         fs.FS
	Auth           *store.Auth
	PublicOrigin   string
	TrustedProxies []netip.Prefix
}

// New requires a real frontend build and the transactional authentication service.
func New(cfg Config) (http.Handler, error) {
	assets := cfg.Assets
	if assets == nil {
		return nil, fmt.Errorf("frontend filesystem is required")
	}
	index, err := fs.Stat(assets, "index.html")
	if err != nil || !index.Mode().IsRegular() || index.Size() == 0 {
		return nil, fmt.Errorf("frontend index.html is missing or invalid; build the frontend first")
	}
	if cfg.Auth == nil {
		return nil, fmt.Errorf("authentication service is required")
	}
	policy, err := newOriginPolicy(cfg.PublicOrigin, cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	locations, err := store.NewLocations(cfg.Auth)
	if err != nil {
		return nil, fmt.Errorf("location service is required: %w", err)
	}
	taxonomy, err := store.NewTaxonomy(cfg.Auth)
	if err != nil {
		return nil, fmt.Errorf("taxonomy service is required: %w", err)
	}
	api := &authAPI{auth: cfg.Auth, locations: locations, taxonomy: taxonomy, origin: policy, limiter: newRequestLimiter()}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := rand.Text()
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS != nil || policy.secure && policy.https(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}

		if r.URL.Path == "/api/v1/health/live" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				methodNotAllowed(w, requestID)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			api.serve(w, r, requestID)
			return
		}
		if privateNamespace(r.URL.Path) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "The API route does not exist.", requestID)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, requestID)
			return
		}
		serveApp(w, r, assets, requestID)
	}), nil
}

func privateNamespace(name string) bool {
	for _, prefix := range []string{"/api", "/auth", "/media", "/backups", "/exports", "/worker", "/data", "/secrets", "/dev"} {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

func serveApp(w http.ResponseWriter, r *http.Request, assets fs.FS, requestID string) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "The resource does not exist.", requestID)
		return
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "The resource does not exist.", requestID)
			return
		}
	}
	extension := strings.ToLower(path.Ext(name))
	if extension == "" {
		// Extensionless client routes, including /assets, resolve to the shell;
		// static directories are never listed and private namespaces never fall back.
		name = "index.html"
	} else if !publicAsset(name, extension) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "The resource does not exist.", requestID)
		return
	}
	info, err := fs.Stat(assets, name)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "The resource does not exist.", requestID)
		return
	}
	if name != "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		if hashedAsset.MatchString(path.Base(name)) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
	}
	http.ServeFileFS(w, r, assets, name)
}

func publicAsset(name, extension string) bool {
	if name == "index.html" {
		return true
	}
	switch extension {
	case ".js", ".css", ".svg", ".png", ".ico", ".webp", ".woff2", ".webmanifest":
		return true
	default:
		return false
	}
}

func methodNotAllowed(w http.ResponseWriter, requestID string) {
	w.Header().Set("Allow", "GET, HEAD")
	writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "This route only accepts GET and HEAD.", requestID)
}

func writeError(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "details": map[string]any{}, "request_id": requestID,
	}})
}
