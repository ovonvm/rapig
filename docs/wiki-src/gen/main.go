package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type heading struct {
	Level int
	Text  string
	ID    string
}

type page struct {
	Slug     string
	File     string
	Title    string
	Chapter  string
	HTML     string
	TOC      []heading
	Plain    string
	Prev     *page
	Next     *page
	NavTitle string
}

var (
	reHeading   = regexp.MustCompile(`^(#{1,4})\s+(.*)$`)
	reChapter   = regexp.MustCompile(`^<!--\s*chapter:\s*(.+?)\s*-->$`)
	reSlug      = regexp.MustCompile(`^<!--\s*slug:\s*([a-z0-9-]+)\s*-->$`)
	reInlineTag = regexp.MustCompile(`<[^>]+>`)
	reSpace     = regexp.MustCompile(`\s+`)
	reCodeFence = regexp.MustCompile("^```")
	reTableSep  = regexp.MustCompile(`^\|?[\s:\-|]+\|?$`)
	reLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reImage     = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	reBold      = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reCode      = regexp.MustCompile("`([^`]+)`")
	reItalic    = regexp.MustCompile(`(^|[^*])\*([^*\n]+)\*`)
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	contentDir := filepath.Join(root, "content")
	outDir := filepath.Join(root, "dist")
	assetsDir := filepath.Join(root, "assets")

	files, err := filepath.Glob(filepath.Join(contentDir, "*.md"))
	if err != nil || len(files) == 0 {
		fatal("no content files in %s", contentDir)
	}
	sort.Strings(files)

	var pages []*page
	for _, f := range files {
		p, err := parsePage(f)
		if err != nil {
			fatal("parse %s: %v", f, err)
		}
		pages = append(pages, p)
	}
	for i := range pages {
		if i > 0 {
			pages[i].Prev = pages[i-1]
		}
		if i < len(pages)-1 {
			pages[i].Next = pages[i+1]
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal("mkdir dist: %v", err)
	}
	copyTree(assetsDir, filepath.Join(outDir, "assets"))

	// chapters in order of appearance
	var chapters []string
	seen := map[string]bool{}
	for _, p := range pages {
		if !seen[p.Chapter] {
			seen[p.Chapter] = true
			chapters = append(chapters, p.Chapter)
		}
	}

	nav := buildNav(pages, chapters)
	for _, p := range pages {
		out := renderPage(p, nav)
		target := filepath.Join(outDir, p.Slug+".html")
		if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
			fatal("write %s: %v", target, err)
		}
	}
	// index.html mirrors the first page but keeps its own URL for links.
	if len(pages) > 0 {
		out := renderPage(pages[0], nav)
		if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(out), 0o644); err != nil {
			fatal("write index: %v", err)
		}
	}
	if err := writeSearchIndex(outDir, pages); err != nil {
		fatal("search index: %v", err)
	}
	fmt.Printf("built %d pages into %s\n", len(pages), outDir)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func slugOf(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".md")
	base = strings.TrimLeft(base, "0123456789-")
	if base == "" {
		return "index"
	}
	return base
}

func parsePage(path string) (*page, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &page{Slug: slugOf(path), File: filepath.Base(path), Chapter: "其他"}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var body strings.Builder
	usedIDs := map[string]int{}
	inCode := false
	var code strings.Builder
	codeLang := ""
	para := []string{}
	list := []string{}
	listOrdered := false
	quote := []string{}

	flushPara := func() {
		if len(para) > 0 {
			body.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	flushList := func() {
		if len(list) > 0 {
			tag := "ul"
			if listOrdered {
				tag = "ol"
			}
			body.WriteString("<" + tag + ">\n")
			for _, item := range list {
				body.WriteString("  <li>" + inline(item) + "</li>\n")
			}
			body.WriteString("</" + tag + ">\n")
			list = nil
			listOrdered = false
		}
	}
	flushQuote := func() {
		if len(quote) > 0 {
			body.WriteString("<blockquote><p>" + inline(strings.Join(quote, " ")) + "</p></blockquote>\n")
			quote = nil
		}
	}
	flush := func() { flushPara(); flushList(); flushQuote() }

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if inCode {
			if reCodeFence.MatchString(trimmed) {
				cls := ""
				if codeLang != "" {
					cls = ` class="language-` + html.EscapeString(codeLang) + `"`
				}
				body.WriteString("<pre><code" + cls + ">" + html.EscapeString(strings.TrimSuffix(code.String(), "\n")) + "</code></pre>\n")
				inCode = false
				code.Reset()
				codeLang = ""
				continue
			}
			code.WriteString(line + "\n")
			continue
		}
		if reCodeFence.MatchString(trimmed) {
			flush()
			inCode = true
			codeLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			continue
		}
		if m := reSlug.FindStringSubmatch(trimmed); m != nil {
			p.Slug = m[1]
			continue
		}
		if m := reChapter.FindStringSubmatch(trimmed); m != nil {
			p.Chapter = m[1]
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		if m := reHeading.FindStringSubmatch(trimmed); m != nil {
			flush()
			level := len(m[1])
			text := strings.TrimSpace(m[2])
			if level == 1 && p.Title == "" {
				p.Title = stripInline(text)
				p.NavTitle = p.Title
				continue
			}
			id := headingID(text, usedIDs)
			p.TOC = append(p.TOC, heading{Level: level, Text: stripInline(text), ID: id})
			fmt.Fprintf(&body, "<h%d id=\"%s\">%s</h%d>\n", level, id, inline(text), level)
			continue
		}
		if trimmed == "---" || trimmed == "***" {
			flush()
			body.WriteString("<hr>\n")
			continue
		}
		if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
			flushPara()
			flushList()
			quote = append(quote, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
			continue
		}
		flushQuote()
		if strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && reTableSep.MatchString(strings.TrimSpace(lines[i+1])) {
			flushPara()
			flushList()
			header := splitRow(trimmed)
			i += 2
			var rows [][]string
			for ; i < len(lines); i++ {
				row := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(row, "|") {
					i--
					break
				}
				rows = append(rows, splitRow(row))
			}
			body.WriteString("<table><thead><tr>")
			for _, h := range header {
				body.WriteString("<th>" + inline(h) + "</th>")
			}
			body.WriteString("</tr></thead><tbody>\n")
			for _, r := range rows {
				body.WriteString("<tr>")
				for _, c := range r {
					body.WriteString("<td>" + inline(c) + "</td>")
				}
				body.WriteString("</tr>\n")
			}
			body.WriteString("</tbody></table>\n")
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			flushPara()
			if listOrdered {
				flushList()
			}
			list = append(list, strings.TrimSpace(trimmed[2:]))
			continue
		}
		if m := regexp.MustCompile(`^(\d+)\.\s+(.*)$`).FindStringSubmatch(trimmed); m != nil {
			flushPara()
			if len(list) > 0 && !listOrdered {
				flushList()
			}
			listOrdered = true
			list = append(list, m[2])
			continue
		}
		flushList()
		para = append(para, trimmed)
	}
	flush()
	if inCode {
		body.WriteString("<pre><code>" + html.EscapeString(code.String()) + "</code></pre>\n")
	}
	p.HTML = body.String()
	p.Plain = strings.Join(strings.Fields(reInlineTag.ReplaceAllString(p.HTML, " ")), " ")
	if p.Title == "" {
		p.Title = p.Slug
		p.NavTitle = p.Title
	}
	return p, nil
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func headingID(text string, used map[string]int) string {
	var b strings.Builder
	for _, r := range stripInline(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
		}
	}
	id := b.String()
	if id == "" {
		id = "section"
	}
	used[id]++
	if n := used[id]; n > 1 {
		id = fmt.Sprintf("%s-%d", id, n)
	}
	return id
}

func stripInline(s string) string {
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$1")
	s = reCode.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$2")
	return strings.TrimSpace(s)
}

func inline(s string) string {
	s = html.EscapeString(s)
	// images first (they look like links)
	s = reImage.ReplaceAllStringFunc(s, func(m string) string {
		sub := reImage.FindStringSubmatch(m)
		alt, src := sub[1], sub[2]
		cls := ""
		if strings.Contains(src, "shot") {
			cls = ` class="shot"`
		}
		return `<img` + cls + ` src="` + src + `" alt="` + alt + `">`
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		text, href := sub[1], sub[2]
		external := strings.HasPrefix(href, "http")
		rel := ""
		if external {
			rel = ` target="_blank" rel="noopener noreferrer"`
		}
		return `<a href="` + href + `"` + rel + `>` + text + `</a>`
	})
	s = reBold.ReplaceAllString(s, `<strong>$1</strong>`)
	s = reCode.ReplaceAllString(s, `<code>$1</code>`)
	s = reItalic.ReplaceAllString(s, `$1<em>$2</em>`)
	return s
}

func buildNav(pages []*page, chapters []string) string {
	assigned := map[string]bool{}
	var b strings.Builder
	for _, ch := range chapters {
		b.WriteString(`<div class="nav-group"><div class="nav-chapter">` + html.EscapeString(ch) + `</div><ul>`)
		for _, p := range pages {
			if p.Chapter != ch || assigned[p.Slug] {
				continue
			}
			assigned[p.Slug] = true
			fmt.Fprintf(&b, `<li><a href="%s" data-slug="%s">%s</a></li>`, pageHref(p.Slug), p.Slug, html.EscapeString(p.NavTitle))
		}
		b.WriteString(`</ul></div>`)
	}
	return b.String()
}

func pageHref(slug string) string {
	if slug == "index" {
		return "./"
	}
	return slug + ".html"
}

func renderPage(p *page, nav string) string {
	var toc strings.Builder
	for _, h := range p.TOC {
		if h.Level > 3 {
			continue
		}
		fmt.Fprintf(&toc, `<a class="toc-l%d" href="#%s">%s</a>`, h.Level, h.ID, html.EscapeString(h.Text))
	}
	prev := ""
	if p.Prev != nil {
		prev = `<a class="pn prev" href="` + pageHref(p.Prev.Slug) + `"><span>上一篇</span>` + html.EscapeString(p.Prev.NavTitle) + `</a>`
	}
	next := ""
	if p.Next != nil {
		next = `<a class="pn next" href="` + pageHref(p.Next.Slug) + `"><span>下一篇</span>` + html.EscapeString(p.Next.NavTitle) + `</a>`
	}
	return `<!doctype html>
<html lang="zh-CN" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(p.Title) + ` · Rapig</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body data-slug="` + p.Slug + `">
<header class="top">
  <button class="icon" id="navToggle" aria-label="目录">☰</button>
  <a class="brand" href="index.html"><span class="logo">◗</span> Rapig</a>
  <span class="docname">Rapig</span>
  <div class="grow"></div>
  <div class="search">
    <input id="q" type="search" placeholder="搜索（/）" autocomplete="off">
    <div class="hits" id="hits" hidden></div>
  </div>
  <button class="icon" id="themeToggle" aria-label="主题">☾</button>
</header>
<div class="shell">
  <aside class="side" id="side">
    <nav>` + nav + `</nav>
  </aside>
  <main class="content">
    <article>
      <h1 class="doctitle">` + html.EscapeString(p.Title) + `</h1>
      ` + p.HTML + `
    </article>
    <div class="pager">` + prev + next + `</div>
  </main>
  <aside class="toc"><div class="toc-title">本页目录</div>` + toc.String() + `</aside>
</div>
<script src="assets/search-index.js"></script>
<script src="assets/app.js"></script>
</body>
</html>
`
}

func writeSearchIndex(outDir string, pages []*page) error {
	type entry struct {
		Slug   string   `json:"s"`
		Title  string   `json:"t"`
		Text   string   `json:"x"`
		Heads  []string `json:"h"`
		Chap   string   `json:"c"`
		NavTag string   `json:"n"`
	}
	var out []entry
	for _, p := range pages {
		var heads []string
		for _, h := range p.TOC {
			heads = append(heads, h.Text)
		}
		out = append(out, entry{Slug: p.Slug, Title: p.Title, Text: truncate(p.Plain, 1200), Heads: heads, Chap: p.Chapter, NavTag: p.NavTitle})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "search-index.json"), data, 0o644); err != nil {
		return err
	}
	// A script copy keeps search working when the site is opened straight from
	// disk (file:// forbids the fetch of the JSON file).
	js := append([]byte("window.RAPIG_WIKI_INDEX = "), data...)
	js = append(js, ";\n"...)
	return os.WriteFile(filepath.Join(outDir, "assets", "search-index.js"), js, 0o644)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func copyTree(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		fatal("read assets: %v", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fatal("mkdir assets: %v", err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(s, d)
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			fatal("read %s: %v", s, err)
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			fatal("write %s: %v", d, err)
		}
	}
}
