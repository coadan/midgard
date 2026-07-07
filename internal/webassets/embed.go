package webassets

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var embedded embed.FS

func HTTPFileSystem() (http.FileSystem, error) {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	return http.FS(dist), nil
}
