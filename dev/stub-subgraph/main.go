// Command stub-subgraph stands in for a GSH/DFM federation subgraph on the
// local stand: it owns Order and references User from the identity
// subgraph. Its job in the spike is to report which identity context
// headers actually reached a subgraph through the router.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"stub-subgraph/generated"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})

	mux := http.NewServeMux()
	mux.Handle("POST /graphql", withHeaders(srv))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("stub-subgraph listening on %s", addr)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * 1e9}
	log.Fatal(server.ListenAndServe())
}
