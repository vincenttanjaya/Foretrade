package main

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/config"
)

// Serving (contract §3): one process serves the API and the built SPAs. With
// ADMIN_PORT, PORT is the public listener (the web app and the API without
// the console's routes) and ADMIN_PORT the console's (the admin SPA, the
// whole API and the OAuth callback). Without it, PORT serves everything.

type adminConnKey struct{}

// listenerContext is the base context of ln's requests; the admin listener's
// are marked so sites can tell them apart on one http.Server.
func listenerContext(ctx context.Context, ln, admin net.Listener) context.Context {
	if admin != nil && ln == admin {
		return context.WithValue(ctx, adminConnKey{}, true)
	}
	return ctx
}

// sites routes each request by the listener it arrived on (listenerContext).
// Without an admin listener it is the single listener of old: the whole API,
// plus WEB_DIR's SPA when set.
func sites(api http.Handler, cfg config.Config, admin net.Listener) http.Handler {
	if admin == nil {
		return site(api, cfg.WebDir, true)
	}
	web, console := site(api, cfg.WebDir, false), site(api, cfg.AdminDir, true)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(adminConnKey{}) != nil {
			console.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
}

// site is one listener's routes: the API's prefixes go to api, the console's
// (/api/admin/, /auth/) only when console is set, else 404; every other path
// is dir's SPA, or the API when dir is "".
func site(api http.Handler, dir string, console bool) http.Handler {
	if dir == "" && console {
		return api
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/healthz", api)
	gated := api
	if !console {
		gated = http.NotFoundHandler() // the console's API and its OAuth callback answer on ADMIN_PORT only
	}
	mux.Handle("/api/admin/", gated)
	mux.Handle("/auth/", gated)
	if dir == "" {
		mux.Handle("/", api)
	} else {
		mux.Handle("/", spa(dir))
	}
	return mux
}

// spa serves dir's files and answers every other path with index.html, so
// the client router resolves deep links. Vite's /assets/ names carry a content
// hash: cached for a year. Everything else revalidates, so a new image shows
// at once. A missing asset is a 404, never index.html: a stale page asking
// for a replaced bundle must not cache HTML as its script. No site may frame
// either console (clickjacking).
// ponytail: no compression; put a proxy in front or precompress when bandwidth matters
func spa(dir string) http.Handler {
	root := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/") // never above root: Clean drops leading ".."
		asset := strings.HasPrefix(name, "assets/")
		if st, err := fs.Stat(root, name); err != nil || st.IsDir() {
			if asset {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}
		if asset {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, root, name) // also refuses ".." left in the path with a 400
	})
}

// serveAdmin serves srv on admin in the background when there is one; an
// unexpected failure calls stop. The channel yields that error once Serve returns.
func serveAdmin(srv *http.Server, admin net.Listener, stop func()) <-chan error {
	done := make(chan error, 1)
	if admin == nil {
		done <- nil
		return done
	}
	slog.Info("listening", "addr", admin.Addr().String(), "listener", "admin")
	go func() {
		err := srv.Serve(admin)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		} else {
			stop()
		}
		done <- err
	}()
	return done
}

// healthcheck is "api healthcheck", the image's HEALTHCHECK (distroless has
// no shell or curl): 0 when this container's /healthz on PORT answers 200.
func healthcheck() int {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://127.0.0.1:" + cmp.Or(os.Getenv("PORT"), "8080") + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
