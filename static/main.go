package main

import (
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bokwoon95/nb6"
	"github.com/oklog/ulid/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

var base32Encoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

func main() {
	// 01jfdk6q-a-whole-new-world
	fmt.Printf("UUIDv7: %s\n", nb6.NewStringID())
	var unixepoch [8]byte
	binary.BigEndian.PutUint64(unixepoch[:], uint64(time.Now().Unix()))
	fmt.Printf("len %d: %s, %s\n", len(unixepoch[0:]), hex.EncodeToString(unixepoch[0:]), base32Encoding.EncodeToString(unixepoch[0:]))
	fmt.Printf("len %d: %s, %s\n", len(unixepoch[1:]), hex.EncodeToString(unixepoch[1:]), base32Encoding.EncodeToString(unixepoch[1:]))
	fmt.Printf("len %d: %s, %s\n", len(unixepoch[2:]), hex.EncodeToString(unixepoch[2:]), base32Encoding.EncodeToString(unixepoch[2:]))
	fmt.Printf("len %d: %s, %s\n", len(unixepoch[3:]), hex.EncodeToString(unixepoch[3:]), base32Encoding.EncodeToString(unixepoch[3:]))
	fmt.Printf("len %d: %s, %s\n", len(unixepoch[4:]), hex.EncodeToString(unixepoch[4:]), base32Encoding.EncodeToString(unixepoch[4:]))
	fmt.Printf("ulid: %s\n", strings.ToLower(ulid.Make().String()))
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
