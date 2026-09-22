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

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{store: NewStore()}}))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})

	mux := http.NewServeMux()
	mux.Handle("POST /graphql", withIdentity(srv))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	log.Printf("messenger listening on %s", addr)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
