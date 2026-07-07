package server

import (
	"net/http"

	"midgard/internal/server/api"
	"midgard/internal/webassets"
)

type Server struct {
	handler http.Handler
}

func New(root string) (*Server, error) {
	apiServer, err := api.New(root)
	if err != nil {
		return nil, err
	}
	staticFS, err := webassets.HTTPFileSystem()
	if err != nil {
		return nil, err
	}
	return &Server{handler: staticHandler(apiServer.Handler(), staticFS)}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
