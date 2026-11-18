// Command docsgen turns the markdown in docs/{user,admin,developer} into the HTML fragments and
// the index that the web frontend embeds (pkg/frontend/docsite). It runs at build time through
// go:generate, so the binary never contains a markdown library.
//
//	go run ./tools/docsgen -docs docs -out pkg/frontend/docsite
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// repoBlob is where a link to a file outside docs/ points.
const repoBlob = "https://github.com/trueforge-org/dexafix/blob/main/"

// sections lists the audiences in the order the web UI shows them.
var sections = []Section{
	{ID: "user", Title: "Using DexaFix", Blurb: "For people who use the web interface: accounts, scans, readings, estimates and imports."},
	{ID: "admin", Title: "Running a server", Blurb: "For the administrator who installs, configures and runs DexaFix."},
	{ID: "developer", Title: "Developing DexaFix", Blurb: "For people who change the code or write scripts against the JSON API."},
}

// Section is one audience with its pages.
type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Blurb string `json:"blurb"`
	Pages []Page `json:"pages"`
}

// Page is one markdown file.
type Page struct {
	Slug  string  `json:"slug"`
	Title string  `json:"title"`
	TOC   []Entry `json:"toc,omitempty"`
}

// Entry is one heading of a page, for its table of contents.
type Entry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Level int    `json:"level"`
}

// Index is index.json: the sections and where each explicit heading id lives.
type Index struct {
	Sections []Section         `json:"sections"`
	Anchors  map[string]string `json:"anchors"`
}

type parsed struct {
	section, slug, src string // src is the path of the markdown file, relative to the docs folder
	source             []byte
	doc                ast.Node
	ids                map[string]bool
}

// exit ends the process. It is a variable so that a test can run main without ending the test.
var exit = os.Exit

func main() { exit(run(os.Args[1:], os.Stderr)) }

// run is main without the process: it returns the exit code.
func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("docsgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	docs := fs.String("docs", "docs", "folder with the markdown pages")
	out := fs.String("out", "docsite", "folder to write the HTML and index.json to")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := generate(*docs, *out); err != nil {
		fmt.Fprintln(stderr, "docsgen:", err)
		return 1
	}
	return 0
}

var numbered = regexp.MustCompile(`^\d+-(.+)\.md$`)

// generate reads every page and rewrites the output folder.
func generate(docs, out string) error {
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithAttribute()),
	)
	var pages []*parsed
	byFile := map[string]*parsed{}
	for _, sec := range sections {
		files, err := filepath.Glob(filepath.Join(docs, sec.ID, "*.md"))
		if err != nil {
			return err
		}
		sort.Strings(files)
		for _, f := range files {
			m := numbered.FindStringSubmatch(filepath.Base(f))
			if m == nil {
				return fmt.Errorf("%s: the file name must be NN-name.md", f)
			}
			src, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			p := &parsed{section: sec.ID, slug: m[1], src: sec.ID + "/" + filepath.Base(f), source: src, ids: map[string]bool{}}
			p.doc = md.Parser().Parse(text.NewReader(src))
			byFile[p.src] = p
			pages = append(pages, p)
		}
	}
	if len(pages) == 0 {
		return fmt.Errorf("no pages found under %s", docs)
	}

	idx := Index{Anchors: map[string]string{}}
	for _, sec := range sections {
		idx.Sections = append(idx.Sections, Section{ID: sec.ID, Title: sec.Title, Blurb: sec.Blurb})
	}
	explicit := regexp.MustCompile(`\{#([\w-]+)\}\s*$`)
	for _, p := range pages {
		var entries []Entry
		title := ""
		_ = ast.Walk(p.doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			h, ok := n.(*ast.Heading)
			if !ok || !entering {
				return ast.WalkContinue, nil
			}
			v, _ := h.AttributeString("id")
			id := string(v.([]byte))
			p.ids[id] = true
			label := plain(h, p.source)
			if h.Level == 1 && title == "" {
				title = label
			}
			if h.Level == 2 || h.Level == 3 {
				entries = append(entries, Entry{ID: id, Title: label, Level: h.Level})
			}
			return ast.WalkSkipChildren, nil
		})
		if title == "" {
			return fmt.Errorf("%s: no # title", p.src)
		}
		// An id written in the markdown as {#id} is a stable target: it is listed, and unique.
		for _, line := range bytes.Split(p.source, []byte("\n")) {
			if !bytes.HasPrefix(line, []byte("#")) {
				continue
			}
			if m := explicit.FindSubmatch(line); m != nil {
				id := string(m[1])
				if other, dup := idx.Anchors[id]; dup {
					return fmt.Errorf("the heading id %q is used on %s and on %s/%s", id, other, p.section, p.slug)
				}
				idx.Anchors[id] = p.section + "/" + p.slug
			}
		}
		for i := range idx.Sections {
			if idx.Sections[i].ID == p.section {
				idx.Sections[i].Pages = append(idx.Sections[i].Pages, Page{Slug: p.slug, Title: title, TOC: entries})
			}
		}
	}

	_ = os.RemoveAll(out) // a failure shows when the first file is written
	for _, p := range pages {
		var rerr error
		_ = ast.Walk(p.doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if l, ok := n.(*ast.Link); ok && entering {
				dest, err := rewrite(string(l.Destination), p, byFile)
				if err != nil && rerr == nil {
					rerr = fmt.Errorf("%s: %w", p.src, err)
				}
				l.Destination = []byte(dest)
			}
			return ast.WalkContinue, nil
		})
		if rerr != nil {
			return rerr
		}
		var buf bytes.Buffer
		_ = md.Renderer().Render(&buf, p.source, p.doc) // writing to a buffer cannot fail
		html := strings.NewReplacer("<table>", `<div class="scroll"><table>`, "</table>", "</table></div>").Replace(buf.String())
		if err := write(filepath.Join(out, p.section, p.slug+".html"), []byte(html)); err != nil {
			return err
		}
	}
	data, _ := json.MarshalIndent(idx, "", "  ") // plain strings and slices: cannot fail
	return write(filepath.Join(out, "index.json"), append(data, '\n'))
}

func write(name string, data []byte) error {
	return errors.Join(os.MkdirAll(filepath.Dir(name), 0o755), os.WriteFile(name, data, 0o644))
}

// plain returns the text of a node without markup.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
		default:
			b.WriteString(plain(c, src))
		}
	}
	return b.String()
}

// rewrite turns a link of a page into one that works in the app: a page of the documentation
// becomes /docs/<section>/<slug>, a file elsewhere in the repository becomes its GitHub address.
// A link to a page or heading that does not exist is an error.
func rewrite(dest string, from *parsed, byFile map[string]*parsed) (string, error) {
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:") || strings.HasPrefix(dest, "/") {
		return dest, nil
	}
	file, frag, _ := strings.Cut(dest, "#")
	target := from
	if file != "" {
		rel := path.Clean(path.Join(path.Dir(from.src), file))
		switch {
		case rel == "index.md":
			return "/docs", nil
		case strings.HasPrefix(rel, "../"):
			// Outside docs/: relative to the repository root, which is one level up.
			return repoBlob + strings.TrimPrefix(rel, "../") + hash(frag), nil
		}
		t, ok := byFile[rel]
		if !ok {
			return dest, fmt.Errorf("link to a page that does not exist: %s", dest)
		}
		target = t
	}
	if frag != "" && !target.ids[frag] {
		return dest, fmt.Errorf("link to a heading that does not exist: %s", dest)
	}
	if file == "" {
		return "#" + frag, nil
	}
	return "/docs/" + target.section + "/" + target.slug + hash(frag), nil
}

func hash(frag string) string {
	if frag == "" {
		return ""
	}
	return "#" + frag
}
