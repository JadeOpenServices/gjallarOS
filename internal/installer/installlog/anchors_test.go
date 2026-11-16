package installlog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var anchorName = regexp.MustCompile(`^(install|stick|maker)(\.[a-z0-9]+(-[a-z0-9]+)*){2,}$`)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}

// idConstants reads the ID constants from the source, so a code added
// without a registry row fails here instead of logging "unregistered".
func idConstants(t *testing.T) map[string]ID {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "anchors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]ID{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "ID" {
				continue
			}
			for i, name := range value.Names {
				var lit *ast.BasicLit
				if i < len(value.Values) {
					lit, _ = value.Values[i].(*ast.BasicLit)
				}
				if lit == nil {
					t.Errorf("%s is not a plain number", name.Name)
					continue
				}
				n, _ := strconv.Atoi(lit.Value)
				ids[name.Name] = ID(n)
			}
		}
	}
	return ids
}

func TestEveryIDIsRegistered(t *testing.T) {
	ids := idConstants(t)
	for name, id := range ids {
		if _, ok := Lookup(id); !ok {
			t.Errorf("%s (%s) has no registry row", name, id.Code())
		}
	}
	if len(ids) != len(Registry) {
		t.Errorf("%d ID constants, %d registry rows", len(ids), len(Registry))
	}
}

func TestRegistryRows(t *testing.T) {
	ids := map[ID]bool{}
	names := map[string]bool{}
	for _, a := range Registry {
		if ids[a.ID] {
			t.Errorf("%s used twice", a.ID.Code())
		}
		ids[a.ID] = true
		if names[a.Name] {
			t.Errorf("name %s used twice", a.Name)
		}
		names[a.Name] = true
		switch {
		case a.ID >= 1000 && a.ID <= 1999:
			if !strings.HasPrefix(a.Name, "install.") {
				t.Errorf("%s is in the installer range but named %s", a.ID.Code(), a.Name)
			}
		case a.ID >= 2000 && a.ID <= 2999:
			if !strings.HasPrefix(a.Name, "stick.") {
				t.Errorf("%s is in the stick range but named %s", a.ID.Code(), a.Name)
			}
		case a.ID >= 3000 && a.ID <= 3999:
			if !strings.HasPrefix(a.Name, "maker.") {
				t.Errorf("%s is in the maker range but named %s", a.ID.Code(), a.Name)
			}
		default:
			t.Errorf("%s is outside the installer, stick and maker ranges", a.ID.Code())
		}
		if !anchorName.MatchString(a.Name) {
			t.Errorf("%s name %q is not install, stick or maker .area.event", a.ID.Code(), a.Name)
		}
		if strings.TrimSpace(a.Meaning) == "" {
			t.Errorf("%s has no meaning", a.ID.Code())
		}
		for _, f := range a.Fields {
			if f.Meaning == "" || f.Name == "" {
				t.Errorf("%s field %q lacks a name or meaning", a.ID.Code(), f.Name)
			}
			if (f.Kind == KindChoice) != (len(f.Choices) > 0) {
				t.Errorf("%s field %s: choices belong to choice fields only", a.ID.Code(), f.Name)
			}
		}
	}
}

func TestLineChecksFields(t *testing.T) {
	line, err := Line(DiskChosen, Count("disks", 3))
	if err != nil || line != "GJA-1012 install.disk.chosen disks=3" {
		t.Errorf("Line = %q, %v", line, err)
	}
	for _, f := range []Field{Count("other", 1), Bool("disks", true)} {
		if line, err := Line(DiskChosen, f); err == nil || !strings.Contains(line, "<invalid>") {
			t.Errorf("bad field accepted: %q", line)
		}
	}
	if line, err := Line(RunHost, Choice("kind", "/dev/sda")); err == nil || strings.Contains(line, "/dev/sda") {
		t.Errorf("choice outside the set written: %q", line)
	}
	if _, err := Line(ID(1998)); err == nil {
		t.Error("unregistered ID accepted")
	}
}

// TestMarkCallsMatchRegistry checks every installlog.Mark call in the
// installer: the anchor must be a registered constant and each field must
// be declared with that kind; choice values must be literals in the set.
func TestMarkCallsMatchRegistry(t *testing.T) {
	root := repoRoot(t)
	names := idConstants(t)
	calls := 0
	dir := filepath.Join(root, "internal", "installer")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isPkgCall(call.Fun, "installlog", "Mark") {
				return true
			}
			calls++
			where := fset.Position(call.Pos()).String()
			if len(call.Args) == 0 {
				t.Errorf("%s: Mark without an anchor", where)
				return true
			}
			sel, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok {
				t.Errorf("%s: anchor must be an installlog constant", where)
				return true
			}
			id, ok := names[sel.Sel.Name]
			anchor, registered := Lookup(id)
			if !ok || !registered {
				t.Errorf("%s: %s is not a registered anchor", where, sel.Sel.Name)
				return true
			}
			for _, arg := range call.Args[1:] {
				checkFieldArg(t, where, anchor, arg)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("no installlog.Mark call found; the walk is broken")
	}
}

func isPkgCall(fun ast.Expr, pkg, name string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

func checkFieldArg(t *testing.T, where string, anchor Anchor, arg ast.Expr) {
	t.Helper()
	call, ok := arg.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		t.Errorf("%s: field must be installlog.Count/Bool/Choice(\"name\", value)", where)
		return
	}
	kinds := map[string]Kind{"Count": KindCount, "Bool": KindBool, "Choice": KindChoice}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		t.Errorf("%s: field must be an installlog constructor", where)
		return
	}
	kind, ok := kinds[sel.Sel.Name]
	if !ok || !isPkgCall(call.Fun, "installlog", sel.Sel.Name) {
		t.Errorf("%s: field must be an installlog constructor", where)
		return
	}
	nameLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		t.Errorf("%s: field name must be a literal", where)
		return
	}
	name, _ := strconv.Unquote(nameLit.Value)
	var spec *FieldSpec
	for i := range anchor.Fields {
		if anchor.Fields[i].Name == name {
			spec = &anchor.Fields[i]
		}
	}
	if spec == nil || spec.Kind != kind {
		t.Errorf("%s: %s has no %s field %q", where, anchor.ID.Code(), kind, name)
		return
	}
	if kind != KindChoice {
		return
	}
	valueLit, ok := call.Args[1].(*ast.BasicLit)
	if !ok {
		t.Errorf("%s: choice value for %s must be a literal", where, name)
		return
	}
	value, _ := strconv.Unquote(valueLit.Value)
	for _, c := range spec.Choices {
		if c == value {
			return
		}
	}
	t.Errorf("%s: %q is not a choice of %s field %s", where, value, anchor.ID.Code(), name)
}

func TestAnchorDocsAreCurrent(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "install-log-anchors.md")
	want := Markdown()
	if os.Getenv("GJALLAR_UPDATE_DOCS") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Error("docs/install-log-anchors.md is out of date; run GJALLAR_UPDATE_DOCS=1 go test ./internal/installer/installlog")
	}
}

// The stick makers write their anchors from shell and PowerShell, so the
// Go checks above do not see them. Each log line must match the registry:
// code and name together, known fields, choice values as written there,
// counts from a variable or a number. Every maker anchor must be written
// by each maker it applies to.
func TestMakerScriptAnchors(t *testing.T) {
	windowsOnly := map[ID]bool{MakerDiskBusy: true}
	for _, c := range []struct {
		file, os    string
		line, count *regexp.Regexp
		skip        map[ID]bool
	}{
		{"make-stick.sh", "linux",
			regexp.MustCompile(`log "GJA-([0-9]{4}) ([a-z0-9.-]+)((?: [a-z-]+=[^ "]+)*)"`),
			regexp.MustCompile(`^(\$[a-z_]+|[0-9]+)$`), windowsOnly},
		{"make-stick.ps1", "windows",
			regexp.MustCompile(`Write-Log ['"]GJA-([0-9]{4}) ([a-z0-9.-]+)((?: [a-z-]+=[^ '"]+)*)['"]`),
			regexp.MustCompile(`^(\$[A-Za-z]+|\$\(\$[A-Za-z]+\.Count\)|[0-9]+)$`), nil},
	} {
		raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "installation", c.file))
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if n, m := strings.Count(src, "GJA-"), len(c.line.FindAllString(src, -1)); n != m {
			t.Errorf("%s: %d GJA- codes, but only %d in log lines", c.file, n, m)
		}
		seen := map[ID]bool{}
		for _, m := range c.line.FindAllStringSubmatch(src, -1) {
			n, _ := strconv.Atoi(m[1])
			anchor, ok := Lookup(ID(n))
			if !ok || anchor.Name != m[2] {
				t.Errorf("%s %q: GJA-%s %s is not a registered anchor", c.file, m[0], m[1], m[2])
				continue
			}
			seen[anchor.ID] = true
			fields := strings.Fields(m[3])
			if len(fields) != len(anchor.Fields) {
				t.Errorf("%s %q: want %d fields", c.file, m[0], len(anchor.Fields))
			}
			for i, f := range fields {
				name, value, _ := strings.Cut(f, "=")
				if i >= len(anchor.Fields) || anchor.Fields[i].Name != name {
					t.Errorf("%s %q: field %s is not field %d of %s", c.file, m[0], name, i+1, anchor.ID.Code())
					continue
				}
				spec := anchor.Fields[i]
				switch spec.Kind {
				case KindCount:
					if !c.count.MatchString(value) {
						t.Errorf("%s %q: count %s must be a number or a variable", c.file, m[0], name)
					}
				case KindChoice:
					if !slices.Contains(spec.Choices, value) {
						t.Errorf("%s %q: %q is not a choice of %s", c.file, m[0], value, name)
					}
					if name == "os" && value != c.os {
						t.Errorf("%s %q: os must be %s", c.file, m[0], c.os)
					}
				default:
					t.Errorf("%s %q: the maker writes no %s fields", c.file, m[0], spec.Kind)
				}
			}
		}
		for _, a := range Registry {
			if a.ID >= 3000 && a.ID <= 3999 && !seen[a.ID] && !c.skip[a.ID] {
				t.Errorf("%s %s is never written by %s", a.ID.Code(), a.Name, c.file)
			}
		}
	}
}
