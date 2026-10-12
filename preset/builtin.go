package preset

import (
	"embed"
	"io/fs"
)

// The bundled preset pack (by Saša Peršić, MIT like the rest of Foveon Lab).
// Names describe the look only; they deliberately carry no film brands.
//
//go:embed builtin
var builtinFS embed.FS

// Builtin returns the bundled presets as a file system of SPP XML files.
func Builtin() fs.FS {
	sub, _ := fs.Sub(builtinFS, "builtin")
	return sub
}
