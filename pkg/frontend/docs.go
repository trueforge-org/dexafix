package frontend

//go:generate go run ../../tools/docsgen -docs ../../docs -out docsite

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/trueforge-org/dexafix/pkg/shared/must"
)

// docsite holds the documentation as HTML fragments, one per page of docs/, and index.json. It is
// generated from the markdown by tools/docsgen (run `go generate ./pkg/frontend` after changing
// docs/); docs_test.go fails when the committed copy is out of date.
//
//go:embed docsite
var docsite embed.FS

// docsIndex mirrors the index.json that tools/docsgen writes.
type docsIndex struct {
	Sections []docsSection     `json:"sections"`
	Anchors  map[string]string `json:"anchors"` // explicit heading id -> "section/slug"
}

type docsSection struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Blurb string     `json:"blurb"`
	Pages []docsPage `json:"pages"`
}

type docsPage struct {
	Slug  string      `json:"slug"`
	Title string      `json:"title"`
	TOC   []docsEntry `json:"toc"`
}

type docsEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Level int    `json:"level"`
}

// docsView is what the documentation templates draw.
type docsView struct {
	Index   *docsIndex
	Section *docsSection // nil on the landing page
	Page    *docsPage    // nil on the landing page
	Body    template.HTML
}

// loadDocs reads the embedded index. The files are generated and embedded, so a failure is a bug
// that any test finds.
func loadDocs() *docsIndex {
	var idx docsIndex
	must.Do(json.Unmarshal(must.Value(docsite.ReadFile("docsite/index.json")), &idx))
	return &idx
}

// find returns the section and page with the given names.
func (d *docsIndex) find(section, slug string) (*docsSection, *docsPage) {
	for i := range d.Sections {
		if d.Sections[i].ID != section {
			continue
		}
		for j := range d.Sections[i].Pages {
			if d.Sections[i].Pages[j].Slug == slug {
				return &d.Sections[i], &d.Sections[i].Pages[j]
			}
		}
	}
	return nil, nil
}

// url returns the address of the heading with the given id: its page, then the id.
func (d *docsIndex) url(id string) (string, error) {
	page, ok := d.Anchors[id]
	if !ok {
		return "", fmt.Errorf("the documentation has no heading with the id %q", id)
	}
	return "/docs/" + page + "#" + id, nil
}

// help returns a small "?" link to the part of the documentation with the given id. The ids are
// constants in the templates; docs_test.go checks that each one exists.
func (d *docsIndex) help(id string) (template.HTML, error) {
	u, err := d.url(id)
	if err != nil {
		return "", err
	}
	return template.HTML(`<a class="help-link" href="` + template.HTMLEscapeString(u) +
		`" target="_blank" rel="noopener" title="Help: what is this?" aria-label="Help">?</a>`), nil
}

// docsLanding serves /docs: the three audiences and their pages. It is public: signed-in visitors
// get their own navigation.
func (s *Frontend) docsLanding(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	data := pageData{Title: "Documentation · DexaFix", Tab: "docs", User: u, Track: s.trackingFor(u),
		Docs: &docsView{Index: s.docs}}
	s.render(w, "docsindex.html", http.StatusOK, data)
}

// docsArticle serves one page of the documentation.
func (s *Frontend) docsArticle(w http.ResponseWriter, r *http.Request) {
	sec, page := s.docs.find(r.PathValue("section"), r.PathValue("page"))
	if page == nil {
		http.NotFound(w, r)
		return
	}
	body := must.Value(fs.ReadFile(docsite, "docsite/"+sec.ID+"/"+page.Slug+".html"))
	u := currentUser(r)
	data := pageData{Title: page.Title + " · DexaFix documentation", Tab: "docs", User: u, Track: s.trackingFor(u),
		Docs: &docsView{Index: s.docs, Section: sec, Page: page, Body: template.HTML(body)}}
	s.render(w, "docs.html", http.StatusOK, data)
}
