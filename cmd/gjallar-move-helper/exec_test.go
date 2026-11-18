package main

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/JadeOpenServices/gjallarOS"

// starters are the names that start a program, by import path. Every name
// of os/exec counts, so a hand-built exec.Cmd does too. The system call
// numbers that make a process count wherever they are used.
var starters = map[string]func(string) bool{
	"os/exec": func(string) bool { return true },
	"os":      oneOf("StartProcess"),
	"syscall": oneOf("Exec", "ForkExec", "StartProcess",
		"SYS_EXECVE", "SYS_EXECVEAT", "SYS_FORK", "SYS_VFORK", "SYS_CLONE", "SYS_CLONE3"),
	"golang.org/x/sys/unix": oneOf("Exec",
		"SYS_EXECVE", "SYS_EXECVEAT", "SYS_FORK", "SYS_VFORK", "SYS_CLONE", "SYS_CLONE3"),
}

// rawCalls take any system call number, so a plain number could hide a
// starter from the names above.
var rawCalls = map[string]func(string) bool{
	"syscall":               oneOf("Syscall", "Syscall6", "RawSyscall", "RawSyscall6"),
	"golang.org/x/sys/unix": oneOf("Syscall", "Syscall6", "RawSyscall", "RawSyscall6", "SyscallNoError", "RawSyscallNoError"),
}

func oneOf(names ...string) func(string) bool {
	return func(n string) bool { return slices.Contains(names, n) }
}

// refused imports hide what a file can do from this test: C code, loaded
// plugins, and unsafe, which with go:linkname (also refused) reaches into
// the runtime. A file in linkedRaw may import unsafe to pass its buffer.
var refused = []string{"C", "plugin", "unsafe"}

// linkedStarters are the files linked into the helper that name a starter,
// with how often. oldroot runs every tool of the helper by fixed path with
// a fixed environment (L114); open.go names exec.ExitError only. The other
// three come with packages the helper links for a type or a pure parser
// only (targetdisk.Result, sshsig.ParseKey); what leads to their starters
// is in notCalled. A change here runs as root unless proven otherwise: it
// needs a root loophole review first.
var linkedStarters = map[string]int{
	"internal/installer/move/oldroot/open.go":     1,
	"internal/installer/move/oldroot/tools.go":    2,
	"internal/installer/targetdisk/discover.go":   1,
	"internal/installer/targetdisk/targetdisk.go": 1,
	"internal/sshsig/sshsig.go":                   2,
}

// linkedRaw are the files that make a raw system call, each by a fixed
// number: utimensat in safefs, as no wrapper takes an fd with no path, and
// the HCIGETDEVINFO ioctl in btaddr.
var linkedRaw = map[string]int{
	"internal/safefs/copy.go":            1,
	"internal/usbtrust/btaddr/btaddr.go": 1,
}

// calledStarters is the one package whose starters the helper may reach.
const calledStarters = "internal/installer/move/oldroot"

// notCalled are the exported names of the other packages above that lead
// to a starter: all of them run lsblk, findmnt or ssh-keygen. Nothing
// linked into the helper outside their own package may name them.
var notCalled = []string{
	"internal/installer/targetdisk.CheckSaveDrive",
	"internal/installer/targetdisk.Discover",
	"internal/installer/targetdisk.MountedDisk",
	"internal/installer/targetdisk.MountedUSBReason",
	"internal/installer/targetdisk.PartitionByLabel",
	"internal/installer/targetdisk.SaveDrives",
	"internal/installer/targetdisk.USBAttached",
	"internal/installer/targetdisk.USBSystemDiskReason",
	"internal/installer/targetdisk.Validate",
	"internal/sshsig.Verify",
}

type file struct {
	pkg, path string
	ast       *ast.File
	imports   map[string]string // local name → import path
}

// L114 (baka-c7 c): every place in the helper's own packages, and in
// everything they link, that can start a program is known and reviewed,
// and only oldroot's are called.
func TestOnlyReviewedFilesStartPrograms(t *testing.T) {
	files := linked(t)

	found, raw := map[string]int{}, map[string]int{}
	for _, f := range files {
		if n := count(f.ast, f.imports, starters); n > 0 {
			found[f.path] = n
		}
		if n := count(f.ast, f.imports, rawCalls); n > 0 {
			raw[f.path] = n
		}
	}
	if !maps.Equal(found, linkedStarters) {
		t.Errorf("files linked into the helper that start programs:\n%v\nreviewed:\n%v", found, linkedStarters)
	}
	if !maps.Equal(raw, linkedRaw) {
		t.Errorf("files linked into the helper that make raw system calls:\n%v\nreviewed:\n%v", raw, linkedRaw)
	}

	byPkg := map[string][]file{}
	for _, f := range files {
		byPkg[f.pkg] = append(byPkg[f.pkg], f)
	}
	var leads []string
	for pkg, fs := range byPkg {
		if pkg == calledStarters {
			continue
		}
		for _, name := range leadToStarter(fs) {
			leads = append(leads, pkg+"."+name)
		}
	}
	slices.Sort(leads)
	if !slices.Equal(leads, notCalled) {
		t.Errorf("exported names that lead to a starter:\n%s\nreviewed:\n%s",
			strings.Join(leads, "\n"), strings.Join(notCalled, "\n"))
	}

	for _, f := range files {
		for _, use := range namesUsed(f) {
			if slices.Contains(leads, use) && !strings.HasPrefix(use, f.pkg+".") {
				t.Errorf("%s names %s, which starts a program", f.path, use)
			}
		}
	}
}

// linked parses every file of the module that is built into the helper.
func linked(t *testing.T) []file {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx := build.Default
	ctx.CgoEnabled = false
	var files []file
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
			path := rel + "/" + name
			if !strings.HasSuffix(name, ".go") {
				t.Fatalf("%s: only Go source can be checked here", path)
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range f.Comments {
				for _, c := range g.List {
					if strings.HasPrefix(c.Text, "//go:linkname") {
						t.Fatalf("%s: go:linkname", path)
					}
				}
			}
			imports := map[string]string{}
			for _, imp := range f.Imports {
				ipath, _ := strconv.Unquote(imp.Path.Value)
				if _, ok := linkedRaw[path]; slices.Contains(refused, ipath) && !(ipath == "unsafe" && ok) {
					t.Fatalf("%s imports %q", path, ipath)
				}
				local := filepath.Base(ipath)
				if imp.Name != nil {
					local = imp.Name.Name
				}
				if local == "." {
					t.Fatalf("%s: dot import of %q", path, ipath)
				}
				imports[local] = ipath
				if after, ok := strings.CutPrefix(ipath, module+"/"); ok {
					queue = append(queue, after)
				}
			}
			files = append(files, file{pkg: rel, path: path, ast: f, imports: imports})
		}
	}
	if !seen[calledStarters] {
		t.Fatal("the walk never reached oldroot; the test is broken")
	}
	return files
}

// count returns how often n names one of names through the file's imports.
func count(n ast.Node, imports map[string]string, names map[string]func(string) bool) int {
	c := 0
	ast.Inspect(n, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			if is, ok := names[imports[id.Name]]; ok && is(sel.Sel.Name) {
				c++
			}
		}
		return true
	})
	return c
}

// leadToStarter returns the exported names of one package that lead to a
// starter. A declaration leads to one when it names a starter or names a
// declaration of its package that does. Methods count under their
// receiver's type, so a value of that type leads there too. Names are
// matched by spelling, which can only find too many.
func leadToStarter(fs []file) []string {
	type decl struct {
		names []string
		node  ast.Node
		file  file
	}
	var decls []decl
	for _, f := range fs {
		for _, d := range f.ast.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				name := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) == 1 {
					name = recvType(d.Recv.List[0].Type)
				}
				decls = append(decls, decl{[]string{name}, d, f})
			case *ast.GenDecl:
				for _, s := range d.Specs {
					var names []string
					switch s := s.(type) {
					case *ast.ValueSpec:
						for _, n := range s.Names {
							names = append(names, n.Name)
						}
					case *ast.TypeSpec:
						names = []string{s.Name.Name}
					default:
						continue
					}
					decls = append(decls, decl{names, s, f})
				}
			}
		}
	}
	leads := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, d := range decls {
			if leads[d.names[0]] {
				continue
			}
			hit := count(d.node, d.file.imports, starters) > 0
			ast.Inspect(d.node, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && leads[id.Name] {
					hit = true
				}
				return !hit
			})
			if hit {
				for _, n := range d.names {
					leads[n] = true
				}
				changed = true
			}
		}
	}
	var exported []string
	for n := range leads {
		if ast.IsExported(n) {
			exported = append(exported, n)
		}
	}
	return exported
}

func recvType(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvType(e.X)
	case *ast.IndexExpr:
		return recvType(e.X)
	case *ast.IndexListExpr:
		return recvType(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

// namesUsed lists every pkg.Name the file names from a module package.
func namesUsed(f file) []string {
	var used []string
	ast.Inspect(f.ast, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			if rel, ok := strings.CutPrefix(f.imports[id.Name], module+"/"); ok {
				used = append(used, rel+"."+sel.Sel.Name)
			}
		}
		return true
	})
	return used
}
