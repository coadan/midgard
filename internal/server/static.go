package server

import (
	"net/http"
	"path"
	"strings"
)

func staticHandler(apiHandler http.Handler, staticFS http.FileSystem) http.Handler {
	fileServer := http.FileServer(staticFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			apiHandler.ServeHTTP(w, r)
			return
		}
		requestPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requestPath == "." || requestPath == "" {
			requestPath = "index.html"
		}
		file, err := staticFS.Open(requestPath)
		if err == nil {
			_ = file.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
