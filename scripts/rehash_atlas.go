//go:build ignore

// rehash_atlas regenerates internal/ent/migrate/migrations/atlas.sum after a migration
// file is added/edited by hand. Run from the service root:
//
//	go run scripts/rehash_atlas.go
package main

import (
	"fmt"
	"log"

	atlasmigrate "ariga.io/atlas/sql/migrate"
)

func main() {
	dir, err := atlasmigrate.NewLocalDir("internal/ent/migrate/migrations")
	if err != nil {
		log.Fatalf("open migrations dir: %v", err)
	}
	sum, err := dir.Checksum()
	if err != nil {
		log.Fatalf("checksum: %v", err)
	}
	if err := atlasmigrate.WriteSumFile(dir, sum); err != nil {
		log.Fatalf("write atlas.sum: %v", err)
	}
	fmt.Println("atlas.sum regenerated")
}
