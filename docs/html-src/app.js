(() => {
  const sidebar = document.getElementById("docs-sidebar");
  const menu = document.getElementById("menu-button");
  const search = document.getElementById("docs-search");
  const print = document.getElementById("print-button");

  menu?.addEventListener("click", () => {
    const open = document.body.classList.toggle("nav-open");
    menu.setAttribute("aria-expanded", String(open));
  });

  document.addEventListener("click", (event) => {
    if (!document.body.classList.contains("nav-open")) return;
    if (sidebar?.contains(event.target) || menu?.contains(event.target)) return;
    document.body.classList.remove("nav-open");
    menu?.setAttribute("aria-expanded", "false");
  });

  search?.addEventListener("input", () => {
    const query = search.value.trim().toLocaleLowerCase();
    document.querySelectorAll(".docs-nav a").forEach((link) => {
      link.hidden = query !== "" && !link.dataset.docTitle.includes(query);
    });
    document.querySelectorAll(".docs-nav section").forEach((section) => {
      section.hidden = !section.querySelector("a:not([hidden])");
    });
  });

  print?.addEventListener("click", () => window.print());

  document.querySelectorAll("pre").forEach((pre) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-code";
    button.textContent = document.documentElement.lang === "zh-CN" ? "复制" : "Copy";
    button.addEventListener("click", async () => {
      const code = pre.querySelector("code")?.textContent || pre.textContent;
      try {
        await navigator.clipboard.writeText(code);
        button.textContent = document.documentElement.lang === "zh-CN" ? "已复制" : "Copied";
      } catch {
        button.textContent = document.documentElement.lang === "zh-CN" ? "复制失败" : "Copy failed";
      }
      window.setTimeout(() => {
        button.textContent = document.documentElement.lang === "zh-CN" ? "复制" : "Copy";
      }, 1600);
    });
    pre.appendChild(button);
  });
})();
