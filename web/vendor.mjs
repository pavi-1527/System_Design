// Copies the pinned browser libraries from node_modules into internal/web/static/vendor,
// where `go:embed` picks them up. The sd binary then serves them itself: no CDN, works
// offline. Run with `npm ci && npm run vendor` after changing a version in package.json.
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const nm = join(here, "node_modules");
const out = join(here, "..", "internal", "web", "static", "vendor");
mkdirSync(out, { recursive: true });

const files = [
  // [package, source path inside the package, destination name]
  ["markdown-it", "dist/browser/markdown-it.umd.min.js", "markdown-it.min.js"],
  ["markdown-it", "LICENSE", "LICENSE.markdown-it.txt"],
  ["mermaid", "dist/mermaid.min.js", "mermaid.min.js"],
  ["mermaid", "LICENSE", "LICENSE.mermaid.txt"],
  ["@highlightjs/cdn-assets", "highlight.min.js", "highlight.min.js"],
  ["@highlightjs/cdn-assets", "styles/github.min.css", "hljs-github.min.css"],
  ["@highlightjs/cdn-assets", "styles/github-dark.min.css", "hljs-github-dark.min.css"],
  ["@highlightjs/cdn-assets", "LICENSE", "LICENSE.highlight.js.txt"],
];

const versions = {};
for (const [pkg, src, dst] of files) {
  const from = join(nm, pkg, src);
  const to = join(out, dst);
  if (dst.endsWith(".js") || dst.endsWith(".css")) {
    // Drop source-map references: the maps aren't vendored, and a dangling reference
    // makes browser dev tools request a file that doesn't exist.
    const text = readFileSync(from, "utf8").replace(/\n?\/[/*][#@] sourceMappingURL=\S+(\s*\*\/)?\s*$/, "\n");
    writeFileSync(to, text);
  } else {
    copyFileSync(from, to);
  }
  versions[pkg] = JSON.parse(readFileSync(join(nm, pkg, "package.json"), "utf8")).version;
}
writeFileSync(
  join(out, "VERSIONS.txt"),
  Object.entries(versions).map(([p, v]) => `${p} ${v}\n`).join(""),
);
console.log("vendored into", out);
for (const [p, v] of Object.entries(versions)) console.log(`  ${p} ${v}`);
