// Applies a yearn.css theme from ?theme=<name> (remembered per browser), and
// fills any <nav data-themes> with links to switch. Loaded as a classic
// script in <head> so the theme stylesheet arrives before first paint.
(() => {
  const BASE = "https://cdn.jsdelivr.net/gh/immannino/yearn.css@0.1.0/dist/themes/";
  const THEMES = ["mint", "dusk", "ember", "tide", "mono", "candy", "arc", "sharebuilder"];

  let theme = new URLSearchParams(location.search).get("theme");
  try {
    if (theme !== null) localStorage.setItem("theme", theme);
    else theme = localStorage.getItem("theme");
  } catch {}
  if (theme && THEMES.includes(theme)) {
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = `${BASE}${theme}.min.css`;
    document.head.append(link); // inserted from a <head> script, so still render-blocking
  }

  document.addEventListener("DOMContentLoaded", () => {
    for (const nav of document.querySelectorAll("nav[data-themes]")) {
      const ul = document.createElement("ul");
      for (const name of ["", ...THEMES]) {
        const a = document.createElement("a");
        const url = new URL(location.href);
        url.searchParams.set("theme", name);
        a.href = url.search;
        a.textContent = name || "default";
        if ((theme || "") === name) a.setAttribute("aria-current", "true");
        const li = document.createElement("li");
        li.append(a);
        ul.append(li);
      }
      nav.append(ul);
    }
  });
})();
