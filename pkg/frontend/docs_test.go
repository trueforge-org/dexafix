package frontend

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	helpCall = regexp.MustCompile(`\{\{\s*help\s+"([^"]+)"\s*\}\}`)
	docsCall = regexp.MustCompile(`\{\{\s*docsURL\s+"([^"]+)"\s*\}\}`)
	idAttr   = regexp.MustCompile(`\bid="([^"]+)"`)
	hrefAttr = regexp.MustCompile(`href="([^"]+)"`)
)

// templateIDs returns every id the templates pass to the given function ({{help "id"}}, {{docsURL "id"}}).
func templateIDs(t *testing.T, call *regexp.Regexp) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := fs.WalkDir(assets, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(data), -1) {
			found[m[1]] = p
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func newDocsFrontend(t *testing.T) *Frontend {
	t.Helper()
	f, err := NewFrontend(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

// The committed HTML must be what tools/docsgen makes of docs/ today: run `go generate ./pkg/frontend`.
func TestGeneratedDocsAreUpToDate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "docsite")
	cmd := exec.Command("go", "run", "../../tools/docsgen", "-docs", "../../docs", "-out", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docsgen failed: %v\n%s", err, msg)
	}
	want := map[string][]byte{}
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		want[filepath.ToSlash(rel)], err = os.ReadFile(p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	have := 0
	err = fs.WalkDir(docsite, "docsite", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		have++
		data, _ := fs.ReadFile(docsite, p)
		name := strings.TrimPrefix(p, "docsite/")
		if !bytes.Equal(data, want[name]) {
			t.Errorf("%s is out of date: run `go generate ./pkg/frontend`", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if have != len(want) {
		t.Errorf("docsite has %d files, docs/ makes %d: run `go generate ./pkg/frontend`", have, len(want))
	}
}

// Every {{help "id"}} and {{docsURL "id"}} in the templates must point at a heading of the documentation.
func TestTemplateDocLinksResolve(t *testing.T) {
	idx := loadDocs()
	used := 0
	for _, call := range []*regexp.Regexp{helpCall, docsCall} {
		for id, file := range templateIDs(t, call) {
			used++
			if _, ok := idx.Anchors[id]; !ok {
				t.Errorf("%s: %q is not a heading id in docs/ (write it as `## Title {#%s}`)", file, id, id)
			}
		}
	}
	if used == 0 {
		t.Fatal("no help links found in the templates")
	}
}

// Every internal link of the generated pages leads to a page, and every in-page link to an element.
func TestGeneratedDocsLinksResolve(t *testing.T) {
	idx := loadDocs()
	pages := 0
	for _, sec := range idx.Sections {
		for _, p := range sec.Pages {
			pages++
			data, err := fs.ReadFile(docsite, "docsite/"+sec.ID+"/"+p.Slug+".html")
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, m := range idAttr.FindAllStringSubmatch(string(data), -1) {
				if ids[m[1]] {
					t.Errorf("%s/%s: duplicate id %q", sec.ID, p.Slug, m[1])
				}
				ids[m[1]] = true
			}
			for _, m := range hrefAttr.FindAllStringSubmatch(string(data), -1) {
				href := m[1]
				switch {
				case strings.HasPrefix(href, "#"):
					if !ids[href[1:]] {
						t.Errorf("%s/%s: link %s has no target", sec.ID, p.Slug, href)
					}
				case strings.HasPrefix(href, "/docs/"):
					path, frag, _ := strings.Cut(strings.TrimPrefix(href, "/docs/"), "#")
					s, slug, _ := strings.Cut(path, "/")
					if _, page := idx.find(s, slug); page == nil {
						t.Errorf("%s/%s: link %s leads to no page", sec.ID, p.Slug, href)
					}
					_ = frag // the generator already checked the heading
				}
			}
		}
	}
	if pages == 0 {
		t.Fatal("no documentation pages")
	}
}

func TestHelpAndDocsURL(t *testing.T) {
	idx := loadDocs()
	got, err := idx.help("provider-client-id")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="/docs/admin/the-admin-page#provider-client-id"`, `class="help-link"`, `rel="noopener"`, `aria-label="Help"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("help link lacks %s: %s", want, got)
		}
	}
	if _, err := idx.help("no-such-heading"); err == nil {
		t.Error("an unknown help id was accepted")
	}
	if u, err := idx.url("rotating-the-key"); err != nil || u != "/docs/admin/encryption-key#rotating-the-key" {
		t.Errorf("url = %q, %v", u, err)
	}
}

func TestDocsPages(t *testing.T) {
	f := newDocsFrontend(t)
	get := func(h http.HandlerFunc, path, section, page string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.SetPathValue("section", section)
		r.SetPathValue("page", page)
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.String()
	}
	code, body := get(f.docsLanding, "/docs", "", "")
	if code != 200 {
		t.Fatalf("landing: %d", code)
	}
	for _, want := range []string{"Using DexaFix", "Running a server", "Developing DexaFix", `href="/docs/user/using-the-app"`, `href="/docs/admin/the-admin-page"`, `href="/docs/developer/json-api"`} {
		if !strings.Contains(body, want) {
			t.Errorf("landing lacks %s", want)
		}
	}
	code, body = get(f.docsArticle, "/docs/admin/configuration", "admin", "configuration")
	if code != 200 {
		t.Fatalf("page: %d", code)
	}
	for _, want := range []string{`id="toc"`, `aria-current="page"`, `<h1`, `data-audience="admin"`, "Running a server"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	for _, c := range [][2]string{{"admin", "nothing"}, {"nobody", "configuration"}} {
		if code, _ := get(f.docsArticle, "/docs/"+c[0]+"/"+c[1], c[0], c[1]); code != 404 {
			t.Errorf("/docs/%s/%s: %d, want 404", c[0], c[1], code)
		}
	}
}
