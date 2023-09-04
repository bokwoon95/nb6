package main

import (
	"encoding/base32"
	"fmt"
	"io"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

func main() {
	fmt.Println(crockfordEncoder.EncodeToString([]byte("The quick brown fox jumps over the lazy dog.")))
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

const crockford32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var crockfordEncoder = base32.NewEncoding(crockford32).WithPadding(base32.NoPadding)
