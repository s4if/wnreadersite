package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeProject creates a minimal project under root: a "{slug}/en" folder
// tree with the given files keyed by "Chapter/name.ext".
func writeProject(t *testing.T, root, slug string, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		path := filepath.Join(root, slug, "en", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// buildServer scans a library root the way main does at startup and returns
// the resulting server.
func buildServer(t *testing.T, root string) *server {
	t.Helper()
	projects, order, err := scanProjects(root, "en", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) == 0 {
		t.Fatal("no projects discovered")
	}
	return &server{
		rootDir:   root,
		siteTitle: "Test Library",
		projects:  projects,
		order:     order,
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wnreader.toml")

	// A missing file is not an error: zero config, everything falls back.
	if cfg := loadConfig(path); cfg != (config{}) {
		t.Errorf("missing file: loadConfig() = %#v, want zero config", cfg)
	}

	src := `
series = "Re:Monster"
author = "Kanekiru Kogitsu"

ext = ".md"

description = '''
Line one.

Line three with "quotes".
'''
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(path)
	if cfg.Series != "Re:Monster" || cfg.Author != "Kanekiru Kogitsu" || cfg.Ext != ".md" {
		t.Errorf("scalar fields = %#v", cfg)
	}
	if want := "Line one.\n\nLine three with \"quotes\"."; cfg.Description != want {
		t.Errorf("description = %q, want %q", cfg.Description, want)
	}

	// A file that fails to parse is ignored rather than fatal.
	if err := os.WriteFile(path, []byte("series ="), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg := loadConfig(path); cfg != (config{}) {
		t.Errorf("invalid file: loadConfig() = %#v, want zero config", cfg)
	}
}

func TestScanProjectsDiscovery(t *testing.T) {
	root := t.TempDir()

	// Real projects.
	writeProject(t, root, "alpha", map[string]string{"Chapter 1/01.txt": "Alpha One\n"})
	if err := os.WriteFile(filepath.Join(root, "alpha", "wnreader.toml"),
		[]byte("series = \"Alpha Series\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProject(t, root, "gamma", map[string]string{"c1/01.md": "Gamma One\n"})

	// Not projects: no pages folder, and a plain file in the root.
	if err := os.MkdirAll(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A symlinked project folder still counts as a project.
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "delta")); err != nil {
		t.Skipf("cannot create symlinks on this system: %v", err)
	}

	projects, order, err := scanProjects(root, "en", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "delta", "gamma"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if got := projects["alpha"].Title; got != "Alpha Series" {
		t.Errorf("alpha title = %q, want it from wnreader.toml", got)
	}
	if got := projects["gamma"].Title; got != "gamma" {
		t.Errorf("gamma title = %q, want folder-name fallback", got)
	}
	if got := projects["delta"]; got == nil || got.BaseDir != projects["alpha"].BaseDir {
		t.Errorf("delta should resolve to alpha's pages folder, got %#v", got)
	}
}

func TestScanProjectsSortsNaturally(t *testing.T) {
	root := t.TempDir()
	for _, slug := range []string{"ch101-ch120", "ch1-ch20", "ch21-ch40"} {
		writeProject(t, root, slug, map[string]string{"c/1.txt": "x\n"})
	}

	_, order, err := scanProjects(root, "en", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ch1-ch20", "ch21-ch40", "ch101-ch120"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestScanProjectsExtPrecedence(t *testing.T) {
	root := t.TempDir()
	// plain uses only .txt files; marked declares ext "md" in its toml.
	writeProject(t, root, "plain", map[string]string{"c/1.txt": "T\n", "c/2.md": "M\n"})
	writeProject(t, root, "marked", map[string]string{"c/1.txt": "T\n", "c/2.md": "M\n"})
	if err := os.WriteFile(filepath.Join(root, "marked", "wnreader.toml"),
		[]byte("ext = \"md\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projects, _, err := scanProjects(root, "en", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(projects["plain"].Pages); got != 1 {
		t.Errorf("plain pages = %d, want 1 (default .txt)", got)
	}
	if got := len(projects["marked"].Pages); got != 1 {
		t.Errorf("marked pages = %d, want 1 (toml ext .md)", got)
	}

	// A global fallback extension applies only where the toml is silent.
	projects, _, err = scanProjects(root, "en", ".md")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(projects["plain"].Pages); got != 1 {
		t.Errorf("plain pages with .md fallback = %d, want 1", got)
	}
	if got := projects["marked"].FileExt; got != ".md" {
		t.Errorf("marked ext = %q, toml value must beat the global flag", got)
	}
}

// do serves a GET request through the real routing table.
func do(s *server, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func TestTruncateDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short keeps as-is", "Blurb here.", 150, "Blurb here."},
		{"exactly max keeps as-is", strings.Repeat("x", 150), 150, strings.Repeat("x", 150)},
		{"over max gets ellipsis", strings.Repeat("x", 200), 150, strings.Repeat("x", 149) + "…"},
		{"counts runes not bytes", strings.Repeat("あ", 160), 150, strings.Repeat("あ", 149) + "…"},
		{"trims trailing space before ellipsis", "word " + strings.Repeat("x", 200), 150, "word " + strings.Repeat("x", 144) + "…"},
		{"zero max yields empty", "anything", 0, ""},
	}
	for _, c := range cases {
		if got := truncateDescription(c.in, c.max); got != c.want {
			t.Errorf("%s: truncateDescription() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHandleHomeListsProjects(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "alpha", map[string]string{"Chapter 1/01.txt": "Alpha One\n"})
	longBlurb := strings.Repeat("a", 300)
	if err := os.WriteFile(filepath.Join(root, "alpha", "wnreader.toml"),
		[]byte("series = \"Alpha Series\"\nauthor = \"Author A\"\ndescription = \""+longBlurb+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProject(t, root, "gamma", map[string]string{"c1/01.md": "Gamma One\n"})

	s := buildServer(t, root)
	response := do(s, "/")

	if response.Code != 200 {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		`<h1 id="home-title">Test Library</h1>`,
		`href="/alpha/"`, "by Author A",
		`href="/gamma/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q:\n%s", want, body)
		}
	}
	// The home page caps the description; the full text must not appear.
	wantBlurb := strings.Repeat("a", 149) + "…"
	if !strings.Contains(body, wantBlurb) {
		t.Errorf("home missing 150-char blurb")
	}
	if strings.Contains(body, strings.Repeat("a", 150)) {
		t.Error("home shows more than 150 characters of description")
	}
	// The chapter index still shows the full description.
	index := do(s, "/alpha/").Body.String()
	if !strings.Contains(index, longBlurb) {
		t.Error("index should show the full description")
	}
}

func TestProjectIndexAndRedirect(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "alpha", map[string]string{
		"Chapter 1/01.txt": "The First Title\nBody\n",
		"Chapter 1/02.txt": "Second\n",
	})
	if err := os.WriteFile(filepath.Join(root, "alpha", "wnreader.toml"),
		[]byte("series = \"Alpha Series\"\nauthor = \"Author A\"\ndescription = \"Blurb here.\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := buildServer(t, root)

	// /{project} redirects to /{project}/.
	response := do(s, "/alpha")
	if response.Code != 301 || response.Header().Get("Location") != "/alpha/" {
		t.Fatalf("redirect = %d %q, want 301 /alpha/", response.Code, response.Header().Get("Location"))
	}
	// An unknown project does not redirect; it is a plain 404.
	response = do(s, "/nope")
	if response.Code != 404 {
		t.Fatalf("unknown project redirect status = %d, want 404", response.Code)
	}

	response = do(s, "/alpha/")
	if response.Code != 200 {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		`<h1 id="index-title">Alpha Series</h1>`,
		"by Author A",
		"Blurb here.",
		`href="/alpha/Chapter%201/01"`,
		`>The First Title</a>`,
		`href="/">← All series</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q:\n%s", want, body)
		}
	}
}

func TestHandlePagePrevNextScopedToProject(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "aaa", map[string]string{
		"c/1.txt": "A One\n", "c/2.txt": "A Two\n", "c/3.txt": "A Three\n",
	})
	writeProject(t, root, "bbb", map[string]string{
		"c/1.txt": "B One\n", "c/2.txt": "B Two\n", "c/3.txt": "B Three\n",
	})
	s := buildServer(t, root)

	get := func(target string) string {
		response := do(s, target)
		if response.Code != 200 {
			t.Fatalf("%s: status = %d, want 200", target, response.Code)
		}
		return response.Body.String()
	}

	// Middle page of aaa links to its own neighbours only.
	body := get("/aaa/c/2")
	for _, want := range []string{`href="/aaa/c/1"`, `href="/aaa/c/3"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "/bbb/") {
		t.Error("page of aaa links into bbb; prev/next must stay within the project")
	}

	// First page of bbb has no previous pointing at aaa's last page.
	body = get("/bbb/c/1")
	if strings.Contains(body, "/aaa/") {
		t.Error("first page of bbb links into aaa; prev/next must stay within the project")
	}

	// Rendered content and title are present.
	if !strings.Contains(body, "B One") || !strings.Contains(body, "<title>c · 1 — bbb</title>") {
		t.Errorf("page body/title wrong:\n%s", body)
	}
}

func TestHandlePageUnknownProjectAndTraversal(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "alpha", map[string]string{"c/1.txt": "One\n"})
	s := buildServer(t, root)

	response := do(s, "/nope/c/1")
	if response.Code != 404 {
		t.Fatalf("unknown project status = %d, want 404", response.Code)
	}

	// A traversal-style folder segment is rejected by safeSegment. The mux
	// would rewrite such a path before routing, so call the handler with the
	// path values set directly — exactly what the mux would pass on.
	req := httptest.NewRequest("GET", "/alpha/x/y", nil)
	req.SetPathValue("project", "alpha")
	req.SetPathValue("folder", "..")
	req.SetPathValue("name", "1")
	response = httptest.NewRecorder()
	s.handlePage(response, req)
	if response.Code != 404 {
		t.Fatalf("traversal status = %d, want 404", response.Code)
	}
}

func TestHandleStyles(t *testing.T) {
	response := httptest.NewRecorder()
	handleStyles(response, httptest.NewRequest("GET", "/styles.css", nil))

	if response.Code != 200 {
		t.Fatalf("handleStyles status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Errorf("Content-Type = %q, want CSS", got)
	}
	if !strings.Contains(response.Body.String(), "@media (max-width: 40rem)") {
		t.Error("embedded stylesheet does not contain the responsive rules")
	}
}

func TestEmbeddedPicoStyles(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "alpha", map[string]string{"c/1.txt": "One\n"})
	s := buildServer(t, root)

	home := do(s, "/").Body.String()
	if !strings.Contains(home, `href="/pico.classless.min.css"`) || strings.Contains(home, "cdn.jsdelivr.net") {
		t.Errorf("home does not use the local Pico stylesheet: %s", home)
	}

	response := do(s, "/pico.classless.min.css")
	if response.Code != 200 {
		t.Fatalf("Pico CSS status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Errorf("Pico CSS Content-Type = %q", got)
	}
	css, err := webAssets.ReadFile("templates/pico.classless.min.css")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response.Body.Bytes(), css) {
		t.Error("Pico route does not serve the embedded stylesheet")
	}

	secured := httptest.NewRecorder()
	securityHeaders(s.routes()).ServeHTTP(secured, httptest.NewRequest("GET", "/", nil))
	if got := secured.Header().Get("Content-Security-Policy"); strings.Contains(got, "cdn.jsdelivr.net") || strings.Contains(got, "fonts.googleapis.com") {
		t.Errorf("CSP still allows third-party styles: %q", got)
	}

	script := do(s, "/reader.js")
	if script.Code != 200 {
		t.Fatalf("reader.js status = %d, want 200", script.Code)
	}
	if got := script.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("reader.js Content-Type = %q", got)
	}
	js, err := webAssets.ReadFile("templates/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(script.Body.Bytes(), js) {
		t.Error("reader.js route does not serve the embedded script")
	}
}

func TestScanPagesUsesFirstNonEmptyLineAsTitle(t *testing.T) {
	baseDir := t.TempDir()
	chapterDir := filepath.Join(baseDir, "Chapter 1")
	if err := os.Mkdir(chapterDir, 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"01.txt":  "\n  \nThe First Title\nBody\n",
		"02.txt":  "\ufeffBOM Title\nBody\n",
		"03.txt":  "\n\n\n\n\nToo Late\n",
		"skip.md": "Not included\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(chapterDir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := scanPages(baseDir, ".txt")
	want := []pageRef{
		{Folder: "Chapter 1", Name: "01", Title: "The First Title"},
		{Folder: "Chapter 1", Name: "02", Title: "BOM Title"},
		{Folder: "Chapter 1", Name: "03", Title: "03"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanPages() = %#v, want %#v", got, want)
	}
}

func TestScanPagesSortsNaturally(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "series", map[string]string{
		"ch1-ch20/1.txt":    "one\n",
		"ch1-ch20/2.txt":    "two\n",
		"ch1-ch20/10.txt":   "ten\n",
		"ch21-ch40/1.txt":   "twenty one\n",
		"ch101-ch120/1.txt": "hundred one\n",
	})

	got := scanPages(filepath.Join(root, "series", "en"), ".txt")
	var folders []string
	var names []string
	for _, p := range got {
		if len(folders) == 0 || folders[len(folders)-1] != p.Folder {
			folders = append(folders, p.Folder)
		}
		if p.Folder == "ch1-ch20" {
			names = append(names, p.Name)
		}
	}

	wantFolders := []string{"ch1-ch20", "ch21-ch40", "ch101-ch120"}
	if !reflect.DeepEqual(folders, wantFolders) {
		t.Errorf("folder order = %v, want %v", folders, wantFolders)
	}
	wantNames := []string{"1", "2", "10"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("page order = %v, want %v", names, wantNames)
	}
}
