package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

func main() {
	var source []byte
	source = []byte(`
# This is a title

This is the **first** paragraph with [a link](http://example.com). This should be extracted.

This is the second paragraph. This should be ignored.
`)
	// source = []byte(`
// This is the **first** paragraph with [a link](http://example.com). This should be extracted.

// This is the second paragraph. This should be ignored.
// `)
	var b strings.Builder
	stripMarkdownStyles(&b, source)
	fmt.Println(b.String())
}

func stripMarkdownStyles(dest io.Writer, source []byte) {
	md := goldmark.New()
	md.Parser().AddOptions(parser.WithAttribute())
	extension.Table.Extend(md)
	md.Renderer().AddOptions(goldmarkhtml.WithUnsafe())
	document := md.Parser().Parse(text.NewReader(source))

	var currentNode ast.Node
	nodeStack := []ast.Node{document}
	for len(nodeStack) > 0 {
		currentNode, nodeStack = nodeStack[len(nodeStack)-1], nodeStack[:len(nodeStack)-1]
		if currentNode == nil {
			continue
		}
		switch currentNode := currentNode.(type) {
		case *ast.Text:
			dest.Write(currentNode.Text(source))
		}
		// if currentNode != document.FirstChild() {
		// 	nodeStack = append(nodeStack, currentNode.NextSibling())
		// }
		nodeStack = append(nodeStack, currentNode.NextSibling())
		nodeStack = append(nodeStack, currentNode.FirstChild())
	}
}
