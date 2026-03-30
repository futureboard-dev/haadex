package engine

import (
	"context"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// Chunk represents a logical code unit extracted by the parser.
type Chunk struct {
	Name    string
	Kind    string
	File    string
	Line    int
	Content string
}

// extractor is a function that walks a tree-sitter AST and returns Chunks.
type extractor func(root *sitter.Node, content []byte) []Chunk

// langConfig pairs a tree-sitter language with its extractor.
type langConfig struct {
	language  *sitter.Language
	extractor extractor
}

// registry maps language IDs to their config.
// Add new languages here — no other changes needed anywhere else.
var registry = map[string]langConfig{
	"go":         {golang.GetLanguage(), extractGoChunks},
	"typescript": {typescript.GetLanguage(), extractTSChunks},
	"tsx":        {tsx.GetLanguage(), extractTSChunks},
	"python":     {python.GetLanguage(), extractPythonChunks},
}

// ParseFile parses source code and extracts top-level symbols as Chunks.
// lang must match a key in registry (e.g. "go", "typescript", "tsx", "python").
func ParseFile(content []byte, lang string) ([]Chunk, error) {
	cfg, ok := registry[lang]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}

	parser := sitter.NewParser()
	parser.SetLanguage(cfg.language)

	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}
	defer tree.Close()

	return cfg.extractor(tree.RootNode(), content), nil
}

// SupportedLanguages returns all registered language IDs.
func SupportedLanguages() []string {
	langs := make([]string, 0, len(registry))
	for k := range registry {
		langs = append(langs, k)
	}
	return langs
}

// ---------------------------------------------------------------------------
// Go extractor
// ---------------------------------------------------------------------------

func extractGoChunks(root *sitter.Node, content []byte) []Chunk {
	var chunks []Chunk

	var walk func(node *sitter.Node)
	walk = func(node *sitter.Node) {
		switch node.Type() {
		case "function_declaration":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "function",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
		case "method_declaration":
			name := fieldContent(node, "name", content)
			recv := goReceiverType(node, content)
			if recv != "" {
				name = recv + "." + name
			}
			chunks = append(chunks, Chunk{
				Name: name, Kind: "method",
				Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
			})
		case "type_declaration":
			for i := 0; i < int(node.NamedChildCount()); i++ {
				spec := node.NamedChild(i)
				if spec.Type() != "type_spec" {
					continue
				}
				name := fieldContent(spec, "name", content)
				kind := "type"
				if t := spec.ChildByFieldName("type"); t != nil {
					switch t.Type() {
					case "struct_type":
						kind = "struct"
					case "interface_type":
						kind = "interface"
					}
				}
				chunks = append(chunks, Chunk{
					Name: name, Kind: kind,
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
			return // don't recurse into type bodies
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			walk(node.NamedChild(i))
		}
	}

	walk(root)
	return chunks
}

// ---------------------------------------------------------------------------
// TypeScript / TSX extractor
// ---------------------------------------------------------------------------

func extractTSChunks(root *sitter.Node, content []byte) []Chunk {
	var chunks []Chunk

	var walk func(node *sitter.Node)
	walk = func(node *sitter.Node) {
		switch node.Type() {
		case "function_declaration", "function":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "function",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
		case "lexical_declaration", "variable_declaration":
			for i := 0; i < int(node.NamedChildCount()); i++ {
				decl := node.NamedChild(i)
				if decl.Type() != "variable_declarator" {
					continue
				}
				name := fieldContent(decl, "name", content)
				val := decl.ChildByFieldName("value")
				if val != nil && (val.Type() == "arrow_function" || val.Type() == "function") {
					chunks = append(chunks, Chunk{
						Name: name, Kind: "function",
						Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
					})
				}
			}
		case "class_declaration":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "class",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
		case "interface_declaration":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "interface",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
		case "type_alias_declaration":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "type",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			walk(node.NamedChild(i))
		}
	}

	walk(root)
	return chunks
}

// ---------------------------------------------------------------------------
// Python extractor
// ---------------------------------------------------------------------------

func extractPythonChunks(root *sitter.Node, content []byte) []Chunk {
	var chunks []Chunk

	var walk func(node *sitter.Node)
	walk = func(node *sitter.Node) {
		switch node.Type() {
		case "function_definition":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "function",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
			return // don't recurse; nested funcs are captured as separate chunks if needed
		case "class_definition":
			if name := fieldContent(node, "name", content); name != "" {
				chunks = append(chunks, Chunk{
					Name: name, Kind: "class",
					Line: int(node.StartPoint().Row) + 1, Content: node.Content(content),
				})
			}
			return
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			walk(node.NamedChild(i))
		}
	}

	walk(root)
	return chunks
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// fieldContent returns the text of a named field child, or "" if absent.
func fieldContent(node *sitter.Node, field string, content []byte) string {
	child := node.ChildByFieldName(field)
	if child == nil {
		return ""
	}
	return child.Content(content)
}

// goReceiverType extracts the type name from a Go method receiver.
func goReceiverType(node *sitter.Node, content []byte) string {
	recv := node.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	for i := 0; i < int(recv.NamedChildCount()); i++ {
		param := recv.NamedChild(i)
		if t := param.ChildByFieldName("type"); t != nil {
			s := t.Content(content)
			if len(s) > 0 && s[0] == '*' {
				s = s[1:]
			}
			return s
		}
	}
	return ""
}
