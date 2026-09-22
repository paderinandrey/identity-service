package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"messenger/generated"
)

// newServer builds the GraphQL handler: POST transport, introspection and
// the Origin guard on mutations (allowed is the ALLOWED_ORIGINS set).
func newServer(store *Store, allowed map[string]bool) *handler.Server {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{store: store}}))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})
	srv.AroundOperations(requireTrustedOrigin(allowed))
	return srv
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := newServer(NewStore(), originSet(os.Getenv("ALLOWED_ORIGINS")))

	mux := http.NewServeMux()
	mux.Handle("POST /graphql", withIdentity(srv))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	log.Printf("messenger listening on %s", addr)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
