//go:build ignore

// Generate the API request contracts from source without executing the API or
// reading its database. Run from the repository root:
//
//	go run ./scripts/generate-agent-contracts.go
//	go run ./scripts/generate-agent-contracts.go -check
//
// Types and JSON tags describe wire shapes only. Business validation, required
// fields, enum values and numeric limits remain the server's responsibility.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type schema map[string]any

type queryParameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
	Schema   schema `json:"schema"`
	Source   string `json:"x-source,omitempty"`
}

type contract struct {
	RequestBody schema           `json:"request_body,omitempty"`
	Queries     []queryParameter `json:"query_parameters"`
	Source      string           `json:"source"`
}

type sourceFile struct {
	path    string
	pkg     string
	imports map[string]string
	ast     *ast.File
}

type typeRef struct {
	expr ast.Expr
	file *sourceFile
}

type function struct {
	decl *ast.FuncDecl
	file *sourceFile
}

type index struct {
	fset  *token.FileSet
	types map[string]typeRef
	funcs map[string]function
}

type environment struct {
	types    map[string]typeRef
	strings  map[string]string
	queries  map[string]bool
	decoders map[string]bool
	receiver string
}

type candidate struct {
	ref    typeRef
	source string
	rank   int
}

type scanResult struct {
	bodies           []candidate
	queries          map[string]queryParameter
	queryDepths      map[string]int
	defaultConflicts map[string]bool
}

func main() {
	check := flag.Bool("check", false, "check generated contracts without modifying files")
	flag.Parse()
	root, err := repositoryRoot()
	if err != nil {
		fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		fatal(err)
	}
	idx, err := readIndex()
	if err != nil {
		fatal(err)
	}
	contracts := map[string]contract{}
	for name, fn := range idx.funcs {
		result := scanResult{queries: map[string]queryParameter{}, queryDepths: map[string]int{}, defaultConflicts: map[string]bool{}}
		idx.scan(fn, nil, nil, &result, map[string]bool{}, 0)
		out := contract{Queries: []queryParameter{}, Source: idx.location(fn.decl.Pos())}
		best := -1
		for _, body := range result.bodies {
			if body.rank <= best {
				continue
			}
			best = body.rank
			out.RequestBody = idx.schema(body.ref, map[string]bool{}, 0)
			out.RequestBody["x-decode-source"] = body.source
			if _, exists := out.RequestBody["x-source"]; !exists {
				out.RequestBody["x-source"] = body.source
			}
		}
		for _, parameter := range result.queries {
			out.Queries = append(out.Queries, parameter)
		}
		sort.Slice(out.Queries, func(i, j int) bool { return out.Queries[i].Name < out.Queries[j].Name })
		contracts[name] = out
	}
	data, err := json.MarshalIndent(contracts, "", "  ")
	if err != nil {
		fatal(err)
	}
	data = append(data, '\n')
	const output = "internal/api/agent_contracts.json"
	if *check {
		current, err := os.ReadFile(output)
		if err != nil {
			fatal(err)
		}
		if !bytes.Equal(current, data) {
			fatal(fmt.Errorf("%s is stale; run go run ./scripts/generate-agent-contracts.go", output))
		}
		fmt.Printf("%s is current (%d functions)\n", output, len(contracts))
		return
	}
	if err := os.WriteFile(output, data, 0644); err != nil {
		fatal(err)
	}
	fmt.Printf("generated %s (%d functions)\n", output, len(contracts))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("cannot find repository go.mod from working directory")
		}
		directory = parent
	}
}

func readIndex() (*index, error) {
	idx := &index{fset: token.NewFileSet(), types: map[string]typeRef{}, funcs: map[string]function{}}
	err := filepath.WalkDir("internal", func(filename string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(idx.fset, filename, nil, 0)
		if err != nil {
			return err
		}
		file := &sourceFile{path: filepath.ToSlash(filename), pkg: filepath.ToSlash(filepath.Dir(filename)), imports: map[string]string{}, ast: parsed}
		for _, imp := range parsed.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			name := path.Base(importPath)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			file.imports[name] = importPath
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					if typ, ok := spec.(*ast.TypeSpec); ok {
						idx.types[file.pkg+"."+typ.Name.Name] = typeRef{typ.Type, file}
					}
				}
			case *ast.FuncDecl:
				if file.pkg == "internal/api" && declaration.Body != nil {
					if declaration.Recv != nil && (len(declaration.Recv.List) == 0 || embeddedName(declaration.Recv.List[0].Type) != "API") {
						continue
					}
					// The API package has unique helper/method names. Reject a
					// collision so generation can never silently pick a body.
					name := declaration.Name.Name
					if _, duplicate := idx.funcs[name]; duplicate {
						return fmt.Errorf("duplicate API function %s", name)
					}
					idx.funcs[name] = function{declaration, file}
				}
			}
		}
		return nil
	})
	return idx, err
}

func (idx *index) location(pos token.Pos) string {
	p := idx.fset.Position(pos)
	return fmt.Sprintf("%s:%d", filepath.ToSlash(p.Filename), p.Line)
}

func unresolved() schema {
	return schema{"description": "由服务端校验", "additionalProperties": true}
}

func (idx *index) schema(ref typeRef, seen map[string]bool, depth int) schema {
	if ref.expr == nil || ref.file == nil || depth > 16 {
		return unresolved()
	}
	switch typ := ref.expr.(type) {
	case *ast.ParenExpr:
		return idx.schema(typeRef{typ.X, ref.file}, seen, depth+1)
	case *ast.StarExpr:
		return schema{"anyOf": []schema{idx.schema(typeRef{typ.X, ref.file}, seen, depth+1), {"type": "null"}}}
	case *ast.Ident:
		switch typ.Name {
		case "string":
			return schema{"type": "string"}
		case "bool":
			return schema{"type": "boolean"}
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
			return schema{"type": "integer"}
		case "float32", "float64":
			return schema{"type": "number"}
		case "any":
			return unresolved()
		}
		return idx.namedSchema(ref.file.pkg+"."+typ.Name, seen, depth)
	case *ast.SelectorExpr:
		qualifier, ok := typ.X.(*ast.Ident)
		if !ok {
			return unresolved()
		}
		importPath := ref.file.imports[qualifier.Name]
		if importPath == "time" && typ.Sel.Name == "Time" {
			return schema{"type": "string", "format": "date-time"}
		}
		if importPath == "encoding/json" && typ.Sel.Name == "RawMessage" {
			return unresolved()
		}
		if strings.HasPrefix(importPath, "ctlvps/") {
			return idx.namedSchema(strings.TrimPrefix(importPath, "ctlvps/")+"."+typ.Sel.Name, seen, depth)
		}
		return unresolved()
	case *ast.ArrayType:
		if element, ok := typ.Elt.(*ast.Ident); ok && (element.Name == "byte" || element.Name == "uint8") {
			// encoding/json encodes byte slices as base64; byte arrays
			// retain their ordinary JSON array representation.
			if typ.Len == nil {
				return schema{"type": "string", "contentEncoding": "base64"}
			}
		}
		return schema{"type": "array", "items": idx.schema(typeRef{typ.Elt, ref.file}, seen, depth+1)}
	case *ast.MapType:
		return schema{"type": "object", "additionalProperties": idx.schema(typeRef{typ.Value, ref.file}, seen, depth+1)}
	case *ast.StructType:
		properties := map[string]schema{}
		unresolvedEmbedded := false
		for _, field := range typ.Fields.List {
			name, options := "", []string{}
			if field.Tag != nil {
				if raw, err := strconv.Unquote(field.Tag.Value); err == nil {
					parts := strings.Split(reflect.StructTag(raw).Get("json"), ",")
					name, options = parts[0], parts[1:]
				}
			}
			if name == "-" {
				continue
			}
			fieldSchema := idx.schema(typeRef{field.Type, ref.file}, seen, depth+1)
			fieldSchema["x-source"] = idx.location(field.Pos())
			if len(field.Names) == 0 && name == "" {
				if embedded, ok := objectProperties(fieldSchema); ok {
					for key, value := range embedded {
						properties[key] = value
					}
				} else {
					// An unresolved embedded type may contribute fields.
					// Keep the overall object open rather than invent names.
					unresolvedEmbedded = true
				}
				continue
			}
			fieldNames := field.Names
			if len(fieldNames) == 0 {
				fieldNames = []*ast.Ident{{Name: embeddedName(field.Type)}}
			}
			for _, fieldName := range fieldNames {
				if !ast.IsExported(fieldName.Name) {
					continue
				}
				jsonName := name
				if jsonName == "" {
					jsonName = fieldName.Name
				}
				if contains(options, "string") {
					fieldSchema = schema{"type": "string", "description": "JSON string 编码；由服务端校验", "x-source": idx.location(field.Pos())}
				}
				properties[jsonName] = fieldSchema
			}
		}
		result := schema{"type": "object", "properties": properties, "additionalProperties": true, "x-source": idx.location(typ.Pos())}
		if unresolvedEmbedded {
			result["description"] = "由服务端校验"
		}
		return result
	case *ast.InterfaceType:
		return unresolved()
	}
	return unresolved()
}

func (idx *index) namedSchema(name string, seen map[string]bool, depth int) schema {
	ref, ok := idx.types[name]
	if !ok || seen[name] {
		return unresolved()
	}
	seen[name] = true
	defer delete(seen, name)
	result := idx.schema(ref, seen, depth+1)
	result["x-source"] = idx.location(ref.expr.Pos())
	return result
}

func objectProperties(value schema) (map[string]schema, bool) {
	if properties, ok := value["properties"].(map[string]schema); ok {
		return properties, true
	}
	if variants, ok := value["anyOf"].([]schema); ok {
		for _, variant := range variants {
			if properties, ok := objectProperties(variant); ok {
				return properties, true
			}
		}
	}
	return nil, false
}

func embeddedName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return expr.Sel.Name
	case *ast.StarExpr:
		return embeddedName(expr.X)
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func newEnvironment(fn function, types []typeRef, values []string) *environment {
	env := &environment{types: map[string]typeRef{}, strings: map[string]string{}, queries: map[string]bool{}, decoders: map[string]bool{}}
	argument := 0
	if fn.decl.Type.Params != nil {
		for _, field := range fn.decl.Type.Params.List {
			for _, name := range field.Names {
				ref := typeRef{field.Type, fn.file}
				if argument < len(types) && types[argument].expr != nil {
					ref = types[argument]
				}
				env.types[name.Name] = ref
				if argument < len(values) && values[argument] != "" {
					env.strings[name.Name] = values[argument]
				}
				argument++
			}
		}
	}
	if fn.decl.Recv != nil && len(fn.decl.Recv.List) > 0 {
		field := fn.decl.Recv.List[0]
		if embeddedName(field.Type) == "API" && len(field.Names) > 0 {
			env.receiver = field.Names[0].Name
		}
	}
	return env
}

func expressionType(expr ast.Expr, file *sourceFile, env *environment) typeRef {
	switch expr := expr.(type) {
	case *ast.Ident:
		return env.types[expr.Name]
	case *ast.UnaryExpr:
		ref := expressionType(expr.X, file, env)
		if expr.Op == token.AND && ref.expr != nil {
			ref.expr = &ast.StarExpr{Star: expr.Pos(), X: ref.expr}
		}
		return ref
	case *ast.StarExpr:
		ref := expressionType(expr.X, file, env)
		if pointer, ok := ref.expr.(*ast.StarExpr); ok {
			ref.expr = pointer.X
		}
		return ref
	case *ast.CompositeLit:
		return typeRef{expr.Type, file}
	case *ast.CallExpr:
		if name, ok := expr.Fun.(*ast.Ident); ok && name.Name == "new" && len(expr.Args) == 1 {
			return typeRef{&ast.StarExpr{Star: expr.Pos(), X: expr.Args[0]}, file}
		}
	case *ast.ParenExpr:
		return expressionType(expr.X, file, env)
	}
	return typeRef{}
}

func stringValue(expr ast.Expr, env *environment) string {
	switch expr := expr.(type) {
	case *ast.BasicLit:
		if expr.Kind == token.STRING {
			value, _ := strconv.Unquote(expr.Value)
			return value
		}
	case *ast.Ident:
		return env.strings[expr.Name]
	}
	return ""
}

func isPackageCall(call *ast.CallExpr, file *sourceFile, importPath, name string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && file.imports[qualifier.Name] == importPath
}

func isURLQuery(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Query" || len(call.Args) != 0 {
		return false
	}
	url, ok := selector.X.(*ast.SelectorExpr)
	return ok && url.Sel.Name == "URL"
}

func (idx *index) helper(call *ast.CallExpr, env *environment) (function, bool) {
	name := ""
	switch target := call.Fun.(type) {
	case *ast.Ident:
		name = target.Name
	case *ast.SelectorExpr:
		// Only follow the API's own receiver. In particular a.Store,
		// a.Traffic and other dependency calls are never traversed.
		if receiver, ok := target.X.(*ast.Ident); ok && receiver.Name == env.receiver && env.receiver != "" {
			name = target.Sel.Name
		}
	}
	fn, ok := idx.funcs[name]
	return fn, ok
}

func (idx *index) scan(fn function, arguments []typeRef, values []string, result *scanResult, seen map[string]bool, depth int) {
	name := fn.decl.Name.Name
	if seen[name] || depth > 12 {
		return
	}
	seen[name] = true
	defer delete(seen, name)
	env := newEnvironment(fn, arguments, values)
	// The declaration pass is intentionally local. It never inspects store
	// implementations or tries to infer a wire schema from returned DB models.
	ast.Inspect(fn.decl.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if node.Type != nil {
					env.types[name.Name] = typeRef{node.Type, fn.file}
				} else if i < len(node.Values) {
					env.types[name.Name] = expressionType(node.Values[i], fn.file, env)
				}
				if i < len(node.Values) {
					bindValue(name.Name, node.Values[i], fn.file, env)
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				name, ok := lhs.(*ast.Ident)
				if !ok || i >= len(node.Rhs) {
					continue
				}
				if inferred := expressionType(node.Rhs[i], fn.file, env); inferred.expr != nil {
					env.types[name.Name] = inferred
				}
				bindValue(name.Name, node.Rhs[i], fn.file, env)
			}
		}
		return true
	})
	ast.Inspect(fn.decl.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		isBody := isPackageCall(call, fn.file, "ctlvps/internal/httpx", "Decode")
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Decode" {
			if receiver, ok := selector.X.(*ast.Ident); ok && env.decoders[receiver.Name] {
				isBody = true
			} else if receiver, ok := selector.X.(*ast.CallExpr); ok && isPackageCall(receiver, fn.file, "encoding/json", "NewDecoder") {
				isBody = true
			}
		}
		if isBody && len(call.Args) > 0 {
			ref := expressionType(call.Args[len(call.Args)-1], fn.file, env)
			// Decode takes a pointer to the JSON value. The pointer used
			// to write into that value does not make its body nullable.
			if pointer, ok := ref.expr.(*ast.StarExpr); ok {
				ref.expr = pointer.X
			}
			rank := 0
			shape := idx.schema(ref, map[string]bool{}, 0)
			if _, ok := shape["type"]; ok {
				rank = 1
			}
			if _, ok := objectProperties(shape); ok {
				rank = 2
			}
			result.bodies = append(result.bodies, candidate{ref, idx.location(call.Pos()), rank})
		}
		idx.scanQuery(call, fn.file, env, result, depth)
		if helper, ok := idx.helper(call, env); ok {
			types := make([]typeRef, len(call.Args))
			strings := make([]string, len(call.Args))
			for i, argument := range call.Args {
				types[i] = expressionType(argument, fn.file, env)
				strings[i] = stringValue(argument, env)
			}
			idx.scan(helper, types, strings, result, seen, depth+1)
		}
		return true
	})
}

func bindValue(name string, value ast.Expr, file *sourceFile, env *environment) {
	if literal := stringValue(value, env); literal != "" {
		env.strings[name] = literal
	}
	if isURLQuery(value) {
		env.queries[name] = true
	}
	if call, ok := value.(*ast.CallExpr); ok && isPackageCall(call, file, "encoding/json", "NewDecoder") {
		env.decoders[name] = true
	}
}

func (idx *index) scanQuery(call *ast.CallExpr, file *sourceFile, env *environment, result *scanResult, depth int) {
	name := ""
	value := schema{"type": "string"}
	int64Query := isPackageCall(call, file, "ctlvps/internal/httpx", "QueryInt64")
	integer := isPackageCall(call, file, "ctlvps/internal/httpx", "QueryInt") || int64Query || isPackageCall(call, file, "ctlvps/internal/httpx", "QueryOffset")
	if integer && len(call.Args) >= 2 {
		name = stringValue(call.Args[1], env)
		value["type"] = "integer"
		if int64Query {
			value["format"] = "int64"
		}
		if len(call.Args) >= 3 {
			if number, ok := integerLiteral(call.Args[2]); ok {
				value["default"] = number
			}
		}
	} else if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Get" && len(call.Args) == 1 {
		query := isURLQuery(selector.X)
		if identifier, ok := selector.X.(*ast.Ident); ok && env.queries[identifier.Name] {
			query = true
		}
		if query {
			name = stringValue(call.Args[0], env)
		}
	}
	if name == "" {
		return
	}
	parameter := queryParameter{Name: name, In: "query", Schema: value, Source: idx.location(call.Pos())}
	previous, exists := result.queries[name]
	// A handler's explicit parameter reads take priority over helper reads:
	// connlogFilter's limit=100 is superseded by summary's limit=15. At the
	// same call depth, typed integer reads are stronger than plain Get calls.
	previousInteger := previous.Schema["type"] == "integer"
	if !exists || depth < result.queryDepths[name] || (depth == result.queryDepths[name] && integer && !previousInteger) {
		result.queries[name] = parameter
		result.queryDepths[name] = depth
		result.defaultConflicts[name] = false
		return
	}
	if depth != result.queryDepths[name] || integer != previousInteger || !integer {
		return
	}
	// Equal-depth reads may describe different branches. If their defaults
	// disagree (or cannot be statically resolved), omit the default instead
	// of advertising whichever read happened to be visited first.
	oldDefault, oldKnown := previous.Schema["default"]
	newDefault, newKnown := parameter.Schema["default"]
	if result.defaultConflicts[name] || oldKnown != newKnown || oldDefault != newDefault {
		delete(previous.Schema, "default")
		previous.Schema["description"] = "默认值由服务端处理分支决定"
		result.defaultConflicts[name] = true
		result.queries[name] = previous
	}
}

func integerLiteral(expr ast.Expr) (int64, bool) {
	sign := int64(1)
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.SUB {
		sign, expr = -1, unary.X
	}
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.ParseInt(literal.Value, 0, 64)
	return sign * value, err == nil
}
