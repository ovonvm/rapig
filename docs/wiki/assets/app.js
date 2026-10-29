(function () {
  const root = document.documentElement;
  const slug = document.body.dataset.slug;
  const KEY = "rapig-wiki-theme";

  function apply(theme) {
    root.setAttribute("data-theme", theme);
    const btn = document.getElementById("themeToggle");
    if (btn) btn.textContent = theme === "dark" ? "☀" : "☾";
  }
  apply(localStorage.getItem(KEY) || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"));

  document.getElementById("themeToggle").addEventListener("click", () => {
    const next = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
    localStorage.setItem(KEY, next);
    apply(next);
  });

  // mark current page in the sidebar
  document.querySelectorAll(".side a[data-slug]").forEach((a) => {
    if (a.dataset.slug === slug) a.classList.add("active");
  });

  // mobile nav
  const side = document.getElementById("side");
  const toggle = document.getElementById("navToggle");
  toggle.addEventListener("click", () => side.classList.toggle("open"));
  document.addEventListener("click", (e) => {
    if (window.innerWidth > 820) return;
    if (!side.contains(e.target) && e.target !== toggle) side.classList.remove("open");
  });

  // search
  const input = document.getElementById("q");
  const hits = document.getElementById("hits");
  let index = null;
  let loading = null;

  function load() {
    if (!loading) {
      if (Array.isArray(window.RAPIG_WIKI_INDEX)) {
        index = window.RAPIG_WIKI_INDEX;
        loading = Promise.resolve(index);
      } else {
        loading = fetch("search-index.json").then((r) => r.json()).then((d) => (index = d));
      }
    }
    return loading;
  }

  function highlight(text, q) {
    const i = text.toLowerCase().indexOf(q);
    if (i < 0) return text;
    return text.slice(0, i) + "**" + text.slice(i, i + q.length) + "**" + text.slice(i + q.length);
  }

  function render(q) {
    q = q.trim().toLowerCase();
    if (!q || !index) { hits.hidden = true; hits.innerHTML = ""; return; }
    const out = [];
    for (const p of index) {
      const inTitle = p.t.toLowerCase().includes(q);
      const headHit = (p.h || []).find((h) => h.toLowerCase().includes(q));
      const bodyHit = (p.x || "").toLowerCase().includes(q);
      if (!inTitle && !headHit && !bodyHit) continue;
      const snippet = bodyHit
        ? (() => {
            const idx = p.x.toLowerCase().indexOf(q);
            const start = Math.max(0, idx - 40);
            return p.x.slice(start, start + 110);
          })()
        : "";
      out.push({ p, inTitle, headHit, snippet });
      if (out.length >= 12) break;
    }
    if (!out.length) { hits.hidden = false; hits.innerHTML = '<div class="c" style="padding:10px">没有匹配的内容</div>'; return; }
    hits.innerHTML = out
      .map(({ p, headHit, snippet }) => {
        const sub = headHit ? headHit : snippet;
        return (
          '<a href="' + p.s + '.html"><div class="h">' + escapeHtml(p.t) + "</div>" +
          '<div class="c">' + escapeHtml(p.c) + (sub ? " · " + escapeHtml(sub) : "") + "</div></a>"
        );
      })
      .join("");
    hits.hidden = false;
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
  }

  input.addEventListener("input", async () => { await load(); render(input.value); });
  input.addEventListener("focus", () => load());
  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== input) { e.preventDefault(); input.focus(); }
    if (e.key === "Escape") { hits.hidden = true; input.blur(); }
  });
  document.addEventListener("click", (e) => {
    if (!hits.contains(e.target) && e.target !== input) hits.hidden = true;
  });
})();
