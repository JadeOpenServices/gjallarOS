package main

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/JadeOpenServices/gjallarOS"

// starters are the calls that start a program, by import path.
var starters = map[string][]string{
	"os/exec":                {"Command", "CommandContext"},
	"os":                     {"StartProcess"},
	"syscall":                {"Exec", "ForkExec", "StartProcess"},
	"golang.org/x/sys/unix": {"Exec"},
}

// linkedStarters are the files linked into the helper that start a
// program. oldroot/tools.go runs every tool of the helper by fixed path
// with a fixed environment (L114). The other three come with packages the
// helper links for a type or a pure parser only (targetdisk.Result,
// sshsig.ParseKey) and are not called from it. A new file here runs as
// root unless proven otherwise: it needs a root loophole review before it
// joins this list.
var linkedStarters = []string{
	"internal/installer/move/oldroot/tools.go",
	"internal/installer/targetdisk/discover.go",
	"internal/installer/targetdisk/targetdisk.go",
	"internal/sshsig/sshsig.go",
}

// L114 (baka-c7 c): every place in the helper's own packages, and in
// everything they link, that can start a program is known and reviewed.
func TestOnlyReviewedFilesStartPrograms(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx := build.Default
	ctx.CgoEnabled = false
	var found []string
	seen := map[string]bool{}
	queue := []string{"cmd/gjallar-move-helper"}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if ok, err := ctx.MatchFile(dir, name); err != nil || !ok {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			local := map[string]string{}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if after, ok := strings.CutPrefix(path, module+"/"); ok {
					queue = append(queue, after)
				}
				if _, ok := starters[path]; ok {
					n := filepath.Base(path)
					if imp.Name != nil {
						n = imp.Name.Name
					}
					local[n] = path
				}
			}
			if starts(f, local) {
				found = append(found, rel+"/"+name)
			}
		}
	}
	if !seen["internal/installer/move/oldroot"] {
		t.Fatal("the walk never reached oldroot; the test is broken")
	}
	slices.Sort(found)
	if !slices.Equal(found, linkedStarters) {
		t.Fatalf("files linked into the helper that start programs:\n%s\nreviewed:\n%s",
			strings.Join(found, "\n"), strings.Join(linkedStarters, "\n"))
	}
}

func starts(f *ast.File, local map[string]string) bool {
	hit := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if path, ok := local[id.Name]; ok && slices.Contains(starters[path], sel.Sel.Name) {
			hit = true
		}
		return true
	})
	return hit
}
