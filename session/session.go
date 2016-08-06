// Package session is the core of gore command
package session

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"

	"golang.org/x/tools/imports"

	"github.com/motemen/go-quickfix"
)

const printerName = "__gore_p"

type Session struct {
	// Parameters used by Init.

	AutoImports bool     // Whether to enable auto imports.
	ExtFiles    []string // List of files to include during Init.
	DotPkg      string   // If not empty, the session dot import the package.

	session // internal state.
}

type session struct {
	// fields computed in init and reset in reset.
	filePath       string
	file           *ast.File
	fset           *token.FileSet
	types          *types.Config
	typeInfo       types.Info
	extraFilePaths []string
	extraFiles     []*ast.File

	mainBody         *ast.BlockStmt
	storedBodyLength int
}

const initialSourceTemplate = `
package main

import %q

func ` + printerName + `(xx ...interface{}) {
	for _, x := range xx {
		%s
	}
}

func main() {
}
`

// printerPkgs is a list of packages that provides
// pretty printing function. Preceding first.
var printerPkgs = []struct {
	path string
	code string
}{
	{"github.com/k0kubun/pp", `pp.Println(x)`},
	{"github.com/davecgh/go-spew/spew", `spew.Printf("%#v\n", x)`},
	{"fmt", `fmt.Printf("%#v\n", x)`},
}

func (s *Session) reset() error {
	s.session = session{}
	return s.Init()
}

func newSession() (*Session, error) {
	s := &Session{}
	return s, s.Init()
}
func (s *Session) Init() error {
	var err error
	s.fset = token.NewFileSet()
	s.types = &types.Config{Importer: importer.Default()}
	s.filePath, err = tempFile()
	if err != nil {
		return err
	}

	var initialSource string
	for _, pp := range printerPkgs {
		_, err := s.types.Importer.Import(pp.path)
		if err == nil {
			initialSource = fmt.Sprintf(initialSourceTemplate, pp.path, pp.code)
			break
		}
		debugf("could not import %q: %s", pp.path, err)
	}

	if initialSource == "" {
		return fmt.Errorf(`Could not load pretty printing package (even "fmt"; something is wrong)`)
	}

	s.file, err = parser.ParseFile(s.fset, "gore_session.go", initialSource, parser.Mode(0))
	if err != nil {
		return err
	}

	s.mainBody = s.mainFunc().Body

	if len(s.ExtFiles) > 0 {
		if err := s.includeFiles(s.ExtFiles); err != nil {
			panic("here" + err.Error())
			return err
		}
	}
	if s.DotPkg != "" {
		if err := s.includePackage(s.DotPkg); err != nil {
			panic("here" + err.Error())
			return err
		}
	}

	return nil
}

func (s *Session) mainFunc() *ast.FuncDecl {
	return s.file.Scope.Lookup("main").Decl.(*ast.FuncDecl)
}

func (s *Session) Run() error {
	f, err := os.Create(s.filePath)
	if err != nil {
		return err
	}

	err = printer.Fprint(f, s.fset, s.file)
	if err != nil {
		return err
	}

	return goRun(append(s.extraFilePaths, s.filePath))
}

func tempFile() (string, error) {
	dir, err := ioutil.TempDir("", "")
	if err != nil {
		return "", err
	}

	err = os.MkdirAll(dir, 0755)
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "gore_session.go"), nil
}

func goRun(files []string) error {
	args := append([]string{"run"}, files...)
	debugf("go %s", strings.Join(args, " "))
	cmd := exec.Command("go", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (s *Session) evalExpr(in string) (ast.Expr, error) {
	expr, err := parser.ParseExpr(in)
	if err != nil {
		return nil, err
	}

	stmt := &ast.ExprStmt{
		X: &ast.CallExpr{
			Fun:  ast.NewIdent(printerName),
			Args: []ast.Expr{expr},
		},
	}

	s.appendStatements(stmt)

	return expr, nil
}

func isNamedIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func (s *Session) evalStmt(in string) error {
	src := fmt.Sprintf("package P; func F() { %s }", in)
	f, err := parser.ParseFile(s.fset, "stmt.go", src, parser.Mode(0))
	if err != nil {
		return err
	}

	enclosingFunc := f.Scope.Lookup("F").Decl.(*ast.FuncDecl)
	stmts := enclosingFunc.Body.List

	if len(stmts) > 0 {
		debugf("evalStmt :: %s", showNode(s.fset, stmts))
		lastStmt := stmts[len(stmts)-1]
		// print last assigned/defined values
		if assign, ok := lastStmt.(*ast.AssignStmt); ok {
			vs := []ast.Expr{}
			for _, v := range assign.Lhs {
				if !isNamedIdent(v, "_") {
					vs = append(vs, v)
				}
			}
			if len(vs) > 0 {
				printLastValues := &ast.ExprStmt{
					X: &ast.CallExpr{
						Fun:  ast.NewIdent(printerName),
						Args: vs,
					},
				}
				stmts = append(stmts, printLastValues)
			}
		}
	}

	s.appendStatements(stmts...)

	return nil
}

func (s *Session) appendStatements(stmts ...ast.Stmt) {
	s.mainBody.List = append(s.mainBody.List, stmts...)
}

type Error string

const (
	ErrContinue Error = "<continue input>"
	ErrQuit     Error = "<quit session>"
)

func (e Error) Error() string {
	return string(e)
}

func (s *Session) source(space bool) (string, error) {
	normalizeNodePos(s.mainFunc())

	var config *printer.Config
	if space {
		config = &printer.Config{
			Mode:     printer.UseSpaces,
			Tabwidth: 4,
		}
	} else {
		config = &printer.Config{
			Tabwidth: 8,
		}
	}

	var buf bytes.Buffer
	err := config.Fprint(&buf, s.fset, s.file)
	return buf.String(), err
}

func (s *Session) reload() error {
	source, err := s.source(false)
	if err != nil {
		return err
	}

	file, err := parser.ParseFile(s.fset, "gore_session.go", source, parser.Mode(0))
	if err != nil {
		return err
	}

	s.file = file
	s.mainBody = s.mainFunc().Body

	return nil
}

func (s *Session) Eval(in string) error {
	debugf("eval >>> %q", in)

	s.clearQuickFix()
	s.storeMainBody()

	var commandRan bool
	for _, command := range commands {
		arg := strings.TrimPrefix(in, ":"+command.name)
		if arg == in {
			continue
		}

		if arg == "" || strings.HasPrefix(arg, " ") {
			arg = strings.TrimSpace(arg)
			err := command.action(s, arg)
			if err != nil {
				if err == ErrQuit {
					return err
				}
				errorf("%s: %s", command.name, err)
			}
			commandRan = true
			break
		}
	}

	if commandRan {
		s.doQuickFix()
		return nil
	}

	if _, err := s.evalExpr(in); err != nil {
		debugf("expr :: err = %s", err)

		err := s.evalStmt(in)
		if err != nil {
			debugf("stmt :: err = %s", err)

			if _, ok := err.(scanner.ErrorList); ok {
				return ErrContinue
			}
		}
	}

	if s.AutoImports {
		s.fixImports()
	}
	s.doQuickFix()

	err := s.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// if failed with status 2, remove the last statement
			if st, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
				if st.ExitStatus() == 2 {
					debugf("got exit status 2, popping out last input")
					s.restoreMainBody()
				}
			}
		}
		errorf("%s", err)
	}

	return err
}

// storeMainBody stores current state of code so that it can be restored
// actually it saves the length of statements inside main()
func (s *Session) storeMainBody() {
	s.storedBodyLength = len(s.mainBody.List)
}

func (s *Session) restoreMainBody() {
	s.mainBody.List = s.mainBody.List[0:s.storedBodyLength]
}

// includeFiles imports packages and funcsions from multiple golang source
func (s *Session) includeFiles(files []string) error {
	for _, file := range files {
		if err := s.includeFile(file, false); err != nil {
			return fmt.Errorf("%q: %v", file, err)
		}
	}
	return nil
}

func (s *Session) includeFile(file string, includingMain bool) error {
	content, err := ioutil.ReadFile(file)
	if err != nil {
		errorf("%s", err)
		return err
	}

	if err = s.importPackages(content); err != nil {
		errorf("%s", err)
		return err
	}

	if err = s.importFile(content, includingMain); err != nil {
		errorf("%s", err)
		return err
	}

	infof("added file %s", file)
	return nil
}

// importPackages includes packages defined on external file into main file
func (s *Session) importPackages(src []byte) error {
	astf, err := parser.ParseFile(s.fset, "", src, parser.Mode(0))
	if err != nil {
		return err
	}

	for _, imt := range astf.Imports {
		debugf("import package: %s", imt.Path.Value)
		actionImport(s, imt.Path.Value)
	}

	return nil
}

// importFile adds external golang file to goRun target to use its function
func (s *Session) importFile(src []byte, includingMain bool) error {
	// Don't need to same directory
	tmp, err := ioutil.TempFile(filepath.Dir(s.filePath), "gore_extarnal_")
	if err != nil {
		return err
	}

	ext := tmp.Name() + ".go"

	f, err := parser.ParseFile(s.fset, ext, src, parser.Mode(0))
	if err != nil {
		return err
	}

	// rewrite to package main
	f.Name.Name = "main"

	// remove func __gore_p(...)
	// remove func main()
	fix := false
	for i, decl := range f.Decls {
		if funcDecl, ok := decl.(*ast.FuncDecl); ok {
			if isNamedIdent(funcDecl.Name, "main") {
				if includingMain {
					// replace
					s.mainFunc().Body = funcDecl.Body
					s.mainBody = funcDecl.Body
				}
				f.Decls = append(f.Decls[0:i], f.Decls[i+1:]...)
				// main() removed from this file, we may have to
				// remove some unsed import's
				fix = true
				continue
			}
			if isNamedIdent(funcDecl.Name, "__gore_p") {
				f.Decls = append(f.Decls[0:i], f.Decls[i+1:]...)
				fix = true
				continue
			}
		}
	}
	if fix {
		quickfix.QuickFix(s.fset, []*ast.File{f})
	}

	out, err := os.Create(ext)
	if err != nil {
		return err
	}
	defer out.Close()

	err = printer.Fprint(out, s.fset, f)
	if err != nil {
		return err
	}

	debugf("import file: %s", ext)
	s.extraFilePaths = append(s.extraFilePaths, ext)
	s.extraFiles = append(s.extraFiles, f)

	return nil
}

// fixImports formats and adjusts imports for the current AST.
func (s *Session) fixImports() error {

	var buf bytes.Buffer
	err := printer.Fprint(&buf, s.fset, s.file)
	if err != nil {
		return err
	}

	formatted, err := imports.Process("", buf.Bytes(), nil)
	if err != nil {
		return err
	}

	s.file, err = parser.ParseFile(s.fset, "", formatted, parser.Mode(0))
	if err != nil {
		return err
	}
	s.mainBody = s.mainFunc().Body

	return nil
}

// includePackage adds the specified package as a '.' import so the session runs as if it is running in the package.
func (s *Session) includePackage(path string) error {
	pkg, err := build.Import(path, ".", 0)
	if err != nil {
		var err2 error
		pkg, err2 = build.ImportDir(path, 0)
		if err2 != nil {
			return err // return package path import error, not directory import error as build.Import can also import directories if "./foo" is specified
		}
	}

	files := make([]string, len(pkg.GoFiles))
	for i, f := range pkg.GoFiles {
		files[i] = filepath.Join(pkg.Dir, f)
	}
	return s.includeFiles(files)
}
