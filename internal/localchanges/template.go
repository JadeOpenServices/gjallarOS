package localchanges

import (
	"embed"
	"io/fs"
	"strings"
)

// The all: prefix keeps the examples, whose names start with _.
//
//go:embed all:template
var templateFiles embed.FS

// Template is the folder developer mode starts with: a README and one
// example module each in system/ and home/, skipped until renamed.
func Template() (Tree, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(templateFiles, "template", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := templateFiles.ReadFile(name)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(name, "template/")] = data
		return nil
	})
	if err != nil {
		return Tree{}, err
	}
	return Check(files)
}
