// Command gen generates typed queries into internal/db/query.
// Run via `go generate ./internal/db` (cwd = internal/db).
package main

import (
	"gorm.io/gen"

	"github.com/yyewolf/ssarchiver/internal/model"
)

func main() {
	g := gen.NewGenerator(gen.Config{
		OutPath: "./query",
		Mode:    gen.WithQueryInterface,
	})
	g.ApplyBasic(model.All()...)
	g.Execute()
}
