// wnreadersite — serve a library of translated works as styled HTML pages.
//
// A self-contained translation reader:
//
//	routes:  /                           project list (the library home)
//	         /{project}/                  chapter index of one series
//	         /{project}/{folder}/{name}   rendered page ({project}/{dir}/{folder}/{name}{ext})
//	config:  {project}/wnreader.toml      optional per-series settings file,
//	                                      looked up in each project folder;
//	                                      missing keys keep their defaults
//	run:     ./wnreadersite [-title "…"] [-port 5500] [-addr 0.0.0.0] [-dir en] [-ext .txt]
//
// The library root is the folder containing the executable. Every direct
// subdirectory of it that contains a pages folder (default "en") is served
// as a project. The whole library is scanned once at startup, so restart
// the app to pick up added or changed projects, pages, or config files.
package main

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	goldmarkhtml "github.com/yuin/goldmark/v2/renderer/html"
)

type pageRef struct {
	Folder string
	Name   string
	Title  string
}

type chapter struct {
	Folder string
	Pages  []pageRef
}

// project is one translation work discovered under the library root. All of
// its state — config, page list, chapter grouping — is resolved once at
// startup and served read-only afterwards.
type project struct {
	Slug        string // directory name under the library root, used in URLs
	Title       string // series title: "series" in wnreader.toml, else the slug
	Author      string // series author shown on the index, "-" when unknown
	Description string // full series blurb shown on the index, "-" when unset
	Blurb       string // description capped at blurbMax characters, for the home page listing
	FileExt     string // extension of translated files, including the dot
	BaseDir     string // absolute, symlink-resolved path of the pages folder
	Pages       []pageRef
	Chapters    []chapter
}

type viewData struct {
	Title       string        // full <title> text
	SiteTitle   string        // library title shown in the header
	ProjectSlug string        // project directory name; "" on the home page
	Author      string        // series author shown on the index ("-" when unknown)
	Description string        // series blurb shown on the index ("-" when unset)
	H1          string        // card heading (page only)
	Body        template.HTML // rendered markdown (page only)
	Prev        *pageRef
	Next        *pageRef
	Chapters    []chapter  // index only
	Projects    []*project // home only
}

// webAssets are compiled into the executable at build time. The chapter text
// remains external and is read from the projects' pages folders at runtime.
//
//go:embed templates/*.html templates/*.css
var webAssets embed.FS

var tmplFuncs = template.FuncMap{"esc": url.PathEscape}

var homeTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(
	webAssets, "templates/base.html", "templates/home.html",
))
var indexTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(
	webAssets, "templates/base.html", "templates/index.html",
))
var pageTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(
	webAssets, "templates/base.html", "templates/page.html",
))

// markdown converter — extensions mirror python-markdown's "extra"
// (tables, fenced code, footnotes, definition lists) plus a few GFM niceties.
// goldmark v2 splits extensions into a parser half and an HTML renderer half;
// WithUnsafe passes raw HTML through, matching python-markdown's behaviour.
var mdParser = parser.New(
	parser.WithExtensions(
		extension.TableParser,
		extension.StrikethroughParser,
		extension.TaskListItemParser,
		extension.LinkifyParser, // parser-only: renders via the ast.AutoLink renderer
		extension.FootnoteParser,
		extension.DefinitionListParser,
	),
)
var mdRenderer = goldmarkhtml.New(
	goldmarkhtml.WithUnsafe(),
	goldmarkhtml.WithExtensions(
		extension.TableHTMLRenderer,
		extension.StrikethroughHTMLRenderer,
		extension.TaskListItemHTMLRenderer,
		extension.FootnoteHTMLRenderer,
		extension.DefinitionListHTMLRenderer,
	),
)

// escapeBoardQuotes replaces every '>' with the HTML entity "&gt;" before the
// text reaches the markdown converter.
//
// Translated board/forum chapters use '>' to mimic the quote symbol of the
// source site (e.g. ">>1" post references), which markdown would otherwise
// turn into <blockquote>s; the entity keeps the '>' visible as literal text.
// The translated files contain no raw HTML, so a global replace is safe.
func escapeBoardQuotes(src []byte) []byte {
	return bytes.ReplaceAll(src, []byte(">"), []byte("&gt;"))
}

// server holds the library state scanned once at startup.
type server struct {
	rootDir   string // absolute path of the library root (the executable's folder)
	siteTitle string // library title shown on the home page
	projects  map[string]*project
	order     []string // project slugs in sorted order for stable listings
}

// config holds the optional per-project settings from a project's
// wnreader.toml. Fields left empty mean "unset"; scanProjects resolves them
// against the global flags and built-in defaults with the precedence
// project toml > global flag > built-in default.
type config struct {
	Series      string `toml:"series"`
	Description string `toml:"description"`
	Author      string `toml:"author"`
	Ext         string `toml:"ext"`
}

// loadConfig reads an optional TOML file. A missing file is not an error: the
// zero config is returned so every setting falls back to its default. A file
// that fails to parse is reported (with the offending line and column) and
// likewise ignored, so a broken config never prevents the app from starting.
func loadConfig(path string) config {
	var cfg config
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("wnreadersite: reading %s: %v", path, err)
		}
		return cfg
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			log.Printf("wnreadersite: parsing %s:\n%s\n(ignoring the file, using defaults)", path, perr.ErrorWithPosition())
		} else {
			log.Printf("wnreadersite: parsing %s: %v (ignoring the file, using defaults)", path, err)
		}
		return config{}
	}
	cfg.Series = strings.TrimSpace(cfg.Series)
	cfg.Description = strings.TrimSpace(cfg.Description)
	cfg.Author = strings.TrimSpace(cfg.Author)
	cfg.Ext = strings.TrimSpace(cfg.Ext)
	log.Printf("wnreadersite: loaded %s", path)
	return cfg
}

const titleScanLines = 5

// blurbMax is the character cap for series descriptions on the home page.
const blurbMax = 150

// truncateDescription caps a blurb at max characters for compact listings.
// It counts runes, not bytes, so multi-byte text is never cut mid-character;
// when it shortens, the last character becomes an ellipsis, so the result
// never exceeds max characters.
func truncateDescription(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// fileTitle returns the first non-empty line among the first titleScanLines
// lines. An empty result tells the caller to fall back to the filename.
func fileTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for line := 0; line < titleScanLines && scanner.Scan(); line++ {
		text := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if text != "" {
			return text
		}
	}
	if err := scanner.Err(); err != nil {
		return ""
	}
	return ""
}

// scanPages returns every (folder, name, title) tuple for files with the
// given extension under baseDir, sorted. Titles come from the first
// non-empty line in the first five lines and fall back to the filename.
func scanPages(baseDir, fileExt string) []pageRef {
	var pages []pageRef
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return pages
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(baseDir, e.Name()))
		if err != nil {
			continue
		}
		names := make([]string, 0, len(files))
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), fileExt) {
				names = append(names, strings.TrimSuffix(f.Name(), fileExt))
			}
		}
		sort.Strings(names)
		for _, n := range names {
			title := fileTitle(filepath.Join(baseDir, e.Name(), n+fileExt))
			if title == "" {
				title = n
			}
			pages = append(pages, pageRef{Folder: e.Name(), Name: n, Title: title})
		}
	}
	return pages
}

// groupChapters groups a sorted page list into chapters by folder, keeping
// first-appearance order (the input is sorted by folder, then name).
func groupChapters(pages []pageRef) []chapter {
	byFolder := map[string]*chapter{}
	var order []string
	for _, p := range pages {
		c, ok := byFolder[p.Folder]
		if !ok {
			c = &chapter{Folder: p.Folder}
			byFolder[p.Folder] = c
			order = append(order, p.Folder)
		}
		c.Pages = append(c.Pages, p)
	}
	chapters := make([]chapter, len(order))
	for i, f := range order {
		chapters[i] = *byFolder[f]
	}
	return chapters
}

// scanProjects walks the library root once at startup and loads every direct
// subdirectory that contains a pages folder as a project. Symlinked project
// folders are followed; entries that are not projects (plain files, or
// directories without a pages folder) are skipped silently so the root can
// also hold the executable and unrelated files. Per-project problems are
// logged and skipped; only a broken root is an error.
func scanProjects(root, pagesDir, extFallback string) (map[string]*project, []string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	projects := make(map[string]*project)
	var order []string
	for _, e := range entries {
		dirPath := filepath.Join(root, e.Name())
		// os.Stat follows symlinks, so a linked project folder still counts
		// and plain files (the binary, notes, …) fail the directory check.
		if st, err := os.Stat(dirPath); err != nil || !st.IsDir() {
			continue
		}
		baseDir := filepath.Join(dirPath, pagesDir)
		if st, err := os.Stat(baseDir); err != nil || !st.IsDir() {
			continue // no pages folder: not a project
		}
		// Resolve once so per-request symlink checks can compare prefixes.
		if resolved, err := filepath.EvalSymlinks(baseDir); err == nil {
			baseDir = resolved
		} else {
			log.Printf("wnreadersite: resolving %s: %v (skipping project)", baseDir, err)
			continue
		}

		slug := e.Name()
		cfg := loadConfig(filepath.Join(dirPath, "wnreader.toml"))
		fileExt, err := normalizeExtension(firstNonEmpty(cfg.Ext, extFallback, ".txt"))
		if err != nil {
			log.Printf("wnreadersite: %s: %v (skipping project)", slug, err)
			continue
		}
		pages := scanPages(baseDir, fileExt)
		desc := firstNonEmpty(cfg.Description, "-")
		projects[slug] = &project{
			Slug:        slug,
			Title:       firstNonEmpty(cfg.Series, slug),
			Author:      firstNonEmpty(cfg.Author, "-"),
			Description: desc,
			Blurb:       truncateDescription(desc, blurbMax),
			FileExt:     fileExt,
			BaseDir:     baseDir,
			Pages:       pages,
			Chapters:    groupChapters(pages),
		}
		order = append(order, slug)
	}
	sort.Strings(order)
	return projects, order, nil
}

func handleStyles(w http.ResponseWriter, _ *http.Request) {
	css, err := webAssets.ReadFile("templates/styles.css")
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(css)
}

func (s *server) handleHome(w http.ResponseWriter, _ *http.Request) {
	list := make([]*project, len(s.order))
	for i, slug := range s.order {
		list[i] = s.projects[slug]
	}
	data := viewData{
		Title:     s.siteTitle,
		SiteTitle: s.siteTitle,
		Projects:  list,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := homeTmpl.Execute(w, data); err != nil {
		log.Printf("home render: %v", err)
	}
}

// handleProjectRedirect sends /{project} to /{project}/ so the pager and
// other absolute links resolve consistently.
func (s *server) handleProjectRedirect(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("project")
	if s.projects[slug] == nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/"+url.PathEscape(slug)+"/", http.StatusMovedPermanently)
}

func (s *server) handleProjectIndex(w http.ResponseWriter, r *http.Request) {
	p := s.projects[r.PathValue("project")]
	if p == nil {
		http.NotFound(w, r)
		return
	}
	data := viewData{
		Title:       p.Title,
		SiteTitle:   s.siteTitle,
		ProjectSlug: p.Slug,
		Author:      p.Author,
		Description: p.Description,
		Chapters:    p.Chapters,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, data); err != nil {
		log.Printf("index render: %v", err)
	}
}

func (s *server) handlePage(w http.ResponseWriter, r *http.Request) {
	p := s.projects[r.PathValue("project")]
	if p == nil {
		http.NotFound(w, r)
		return
	}
	folder := r.PathValue("folder")
	name := r.PathValue("name")
	if !safeSegment(folder) || !safeSegment(name) {
		http.NotFound(w, r)
		return
	}

	// Reject symlinks pointing outside the project's pages folder (defense
	// in depth: the startup scan never lists them, but they could still be
	// requested directly).
	path := filepath.Join(p.BaseDir, folder, name+p.FileExt)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.HasPrefix(resolved, p.BaseDir+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src := escapeBoardQuotes(raw)
	doc := mdParser.Parse(src)
	var buf bytes.Buffer
	if err := mdRenderer.Render(&buf, src, doc); err != nil {
		log.Printf("markdown convert %s/%s/%s: %v", p.Slug, folder, name, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := viewData{
		Title:       fmt.Sprintf("%s · %s — %s", folder, name, p.Title),
		SiteTitle:   s.siteTitle,
		ProjectSlug: p.Slug,
		H1:          fmt.Sprintf("%s · %s", folder, name),
		Body:        template.HTML(buf.String()),
	}
	// Prev/next navigation is scoped to this project's own pages.
	if len(p.Pages) > 0 {
		for i := range p.Pages {
			if p.Pages[i].Folder == folder && p.Pages[i].Name == name {
				if i > 0 {
					data.Prev = &p.Pages[i-1]
				}
				if i < len(p.Pages)-1 {
					data.Next = &p.Pages[i+1]
				}
				break
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, data); err != nil {
		log.Printf("page render: %v", err)
	}
}

// firstNonEmpty returns the first argument that is not "" (or "" if all are).
// Callers use it to apply the "most specific setting wins" precedence
// (project wnreader.toml > global flag > built-in default).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// normalizeExtension makes both "txt" and ".txt" usable as extensions and
// rejects values that could escape the requested folder when appended to a name.
func normalizeExtension(ext string) (string, error) {
	if ext == "" {
		return "", fmt.Errorf("extension must not be empty")
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if ext == "." || strings.ContainsAny(ext, "/\\\x00") {
		return "", fmt.Errorf("invalid extension %q", ext)
	}
	return ext, nil
}

// safeSegment rejects path traversal in a single URL segment.
func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	return true
}

// routes builds the routing table. It is split out of main so tests can
// exercise the full request path, exactly as requests arrive at runtime.
func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /styles.css", handleStyles)
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /{project}", s.handleProjectRedirect)
	mux.HandleFunc("GET /{project}/{$}", s.handleProjectIndex)
	mux.HandleFunc("GET /{project}/{folder}/{name}", s.handlePage)
	return mux
}

// securityHeaders sets response hardening headers on every response.
// CSP allows same-origin styles and chapter images but no scripts — the chapter
// files are trusted, and this is defense in depth if one is ever swapped.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src * data:")
		next.ServeHTTP(w, r)
	})
}

func main() {
	var (
		addr  = flag.String("addr", "0.0.0.0", "address to listen on (0.0.0.0 = all interfaces)")
		port  = flag.Int("port", 5500, "port to listen on")
		title = flag.String("title", "", `library title shown on the home page (default: the library folder's name)`)
		dir   = flag.String("dir", "en", `pages subfolder each project must contain ("{project}/{dir}" structure)`)
		ext   = flag.String("ext", "", `fallback translated file extension, e.g. ".txt" or ".md" (default: "ext" in a project's wnreader.toml, else .txt)`)
	)
	flag.Parse()

	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("wnreadersite: cannot locate executable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	rootDir := filepath.Dir(exe)

	projects, order, err := scanProjects(rootDir, *dir, *ext)
	if err != nil {
		log.Fatalf("wnreadersite: scanning %s: %v", rootDir, err)
	}
	if len(projects) == 0 {
		log.Fatalf("wnreadersite: no projects found under %s\n(each direct subdirectory containing a %q folder is served as a project)", rootDir, *dir)
	}

	s := &server{
		rootDir:   rootDir,
		siteTitle: firstNonEmpty(*title, filepath.Base(rootDir)),
		projects:  projects,
		order:     order,
	}
	for _, slug := range order {
		p := projects[slug]
		log.Printf("wnreadersite: project %q [%s] — %d pages", p.Title, slug, len(p.Pages))
	}

	mux := s.routes()

	listenAddr := fmt.Sprintf("%s:%d", *addr, *port)
	log.Printf("wnreadersite: %d projects in %s", len(projects), rootDir)
	log.Printf("wnreadersite: \"%s\" listening on http://%s", s.siteTitle, listenAddr)
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("wnreadersite: %v", err)
	}
}
