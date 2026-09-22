package main

// Resolver wires the in-memory store into the generated schema.
type Resolver struct {
	store *Store
}
