package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes files (path relative to the docs folder -> content) and returns the folder.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func good() map[string]string {
	return map[string]string{
		"user/01-first.md":      "# First {#first-page}\n\n## Part one\n\nSee [admin](../admin/01-setup.md#the-setup), [here](#part-one), [index](../index.md), [code](../../AGENTS.md#rules) and [web](https://example.com).\n\n| a | b |\n| - | - |\n| 1 | **2** |\n\n### Deep `code` heading\n",
		"admin/01-setup.md":     "# Setup\n\n## The setup {#the-setup}\n\nBack to [first](../user/01-first.md).\n",
		"developer/02-notes.md": "# Notes\n\nTo [abs](/docs) and [mail](mailto:a@example.com).\n",
	}
}

func TestGenerateWritesPagesAndIndex(t *testing.T) {
	docs, out := tree(t, good()), filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "stale.html"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(docs, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "stale.html")); !os.IsNotExist(err) {
		t.Error("a stale file was kept")
	}
	page, _ := os.ReadFile(filepath.Join(out, "user", "first.html"))
	for _, want := range []string{
		`href="/docs/admin/setup#the-setup"`, `href="#part-one"`, `href="/docs"`,
		`href="https://github.com/trueforge-org/dexafix/blob/main/AGENTS.md#rules"`,
		`href="https://example.com"`, `<div class="scroll"><table>`, `id="part-one"`, `id="first-page"`,
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("first.html lacks %s:\n%s", want, page)
		}
	}
	var idx Index
	data, _ := os.ReadFile(filepath.Join(out, "index.json"))
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Anchors["the-setup"] != "admin/setup" || idx.Anchors["first-page"] != "user/first" {
		t.Errorf("anchors = %v", idx.Anchors)
	}
	first := idx.Sections[0].Pages[0]
	if first.Title != "First" || len(first.TOC) != 2 || first.TOC[1].Title != "Deep code heading" || first.TOC[1].Level != 3 {
		t.Errorf("first page = %+v", first)
	}
	if len(idx.Sections) != 3 || idx.Sections[2].Pages[0].Slug != "notes" {
		t.Errorf("sections = %+v", idx.Sections)
	}
}

func TestGenerateFailsClearly(t *testing.T) {
	with := func(name, body string) map[string]string {
		m := good()
		m[name] = body
		return m
	}
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"a link to a missing page":    {with("user/01-first.md", "# T\n\n[x](../admin/09-none.md)\n"), "page that does not exist"},
		"a link to a missing heading": {with("user/01-first.md", "# T\n\n[x](#nowhere)\n"), "heading that does not exist"},
		"a page without a title":      {with("user/01-first.md", "no heading\n"), "no # title"},
		"a file that is not numbered": {with("user/notes.md", "# N\n"), "NN-name.md"},
		"the same explicit id twice":  {with("admin/02-other.md", "# O\n\n## S {#the-setup}\n"), "is used on"},
		"nothing to convert":          {map[string]string{"index.md": "# x\n"}, "no pages found"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := generate(tree(t, c.files), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestGenerateReportsFileSystemFailures(t *testing.T) {
	docs := tree(t, good())
	// A file where a folder must be created.
	out := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(docs, filepath.Join(out, "site")); err == nil {
		t.Error("an output folder below a file did not fail")
	}
	// A directory named like a page.
	bad := tree(t, map[string]string{"user/01-dir.md/x": "x"})
	if err := generate(bad, t.TempDir()); err == nil {
		t.Error("a directory named like a page did not fail")
	}
}

func TestRun(t *testing.T) {
	docs, out := tree(t, good()), t.TempDir()
	var stderr bytes.Buffer
	if code := run([]string{"-docs", docs, "-out", out}, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if code := run([]string{"-docs", t.TempDir(), "-out", out}, &stderr); code != 1 || !strings.Contains(stderr.String(), "docsgen:") {
		t.Errorf("code %d, stderr %q", code, stderr.String())
	}
	if code := run([]string{"-nope"}, &stderr); code != 2 {
		t.Errorf("a bad flag gave code %d", code)
	}
}

func TestMainExitsWithTheCodeOfRun(t *testing.T) {
	oldExit, oldArgs := exit, os.Args
	t.Cleanup(func() { exit, os.Args = oldExit, oldArgs })
	got := -1
	exit = func(code int) { got = code }
	os.Args = []string{"docsgen", "-docs", "["}
	main()
	if got != 1 {
		t.Errorf("exit code %d, want 1 for a docs folder that is not a valid pattern", got)
	}
}
