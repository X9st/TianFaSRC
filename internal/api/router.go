package api

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/system"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/webui"
)

type Options struct {
	Logger    *slog.Logger
	Version   string
	Readiness system.Readiness
	Pool      *pgxpool.Pool
}

const (
	LivePath  = "/health/live"
	ReadyPath = "/health/ready"
)

func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+LivePath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{
			Status: "ok", Service: "tianfa-server", Version: opts.Version,
			Checks: map[string]system.Check{"process": {Status: "up"}},
		})
	})
	mux.HandleFunc("GET "+ReadyPath, func(w http.ResponseWriter, r *http.Request) {
		checks := opts.Readiness.Checks(r.Context())
		status, code := "ok", http.StatusOK
		if !system.AllUp(checks) {
			status, code = "not_ready", http.StatusServiceUnavailable
		}
		writeJSON(w, code, healthResponse{
			Status: status, Service: "tianfa-server", Version: opts.Version, Checks: checks,
		})
	})
	if opts.Pool != nil {
		phase1API{auth: auth.Store{Pool: opts.Pool}, tasks: task.Store{Pool: opts.Pool}}.routes(mux)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "route-not-found", "接口不存在", "请求的 API 路由尚未定义")
	})

	dist, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(dist))
	mux.Handle("/", spaFileHandler(dist, fileServer))

	handler := recoverMiddleware(opts.Logger, mux)
	handler = accessLogMiddleware(opts.Logger, handler)
	handler = requestIDMiddleware(handler)
	return securityHeaders(handler)
}

func spaFileHandler(dist fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeProblem(w, r, http.StatusMethodNotAllowed, "method-not-allowed", "方法不允许", "该资源只支持读取")
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func accessLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.InfoContext(r.Context(), "request completed",
			"request_id", requestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				logger.ErrorContext(r.Context(), "request panic", "request_id", requestID(r.Context()), "error", value)
				writeProblem(w, r, http.StatusInternalServerError, "internal", "服务内部错误", "请求未能完成")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
