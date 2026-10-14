import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { collectNotices } from "./third-party-notices.mjs";
const dist = new URL("../dist/", import.meta.url);
let html = await fs.readFile(new URL("index.html", dist), "utf8");
for (const match of [
  ...html.matchAll(/<script[^>]*src="([^"]+)"[^>]*><\/script>/g),
]) {
  const js = await fs.readFile(new URL(match[1], dist), "utf8");
  html = html.replace(
    match[0],
    () => "<script>" + js.replaceAll("</script", "<\\/script") + "</script>",
  );
}
for (const match of [
  ...html.matchAll(/<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"[^>]*>/g),
]) {
  const css = await fs.readFile(new URL(match[1], dist), "utf8");
  html = html.replace(match[0], () => "<style>" + css + "</style>");
}
// Put the compiled app after the root element so the offline file works without modules.
const scripts = [...html.matchAll(/<script>[\s\S]*?<\/script>/g)].map(
  (x) => x[0],
);
for (const script of scripts) html = html.replace(script, "");
html = html.replace("</body>", () => scripts.join("\n") + "\n</body>");
html = html.replace(
  "<head>",
  "<head>\n<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; base-uri 'none'; form-action 'none'\">",
);
// scripts/ is one level below the preview project.
const output = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../docs/ui-preview.html",
);
const notices = await collectNotices();
await fs.writeFile(
  path.join(path.dirname(output), "third-party-notices.txt"),
  notices,
);
html = html.replace(
  "</body>",
  () =>
    '<script type="application/json" id="third-party-notices">' +
    JSON.stringify({ notices }).replaceAll("<", "\\u003c") +
    "</script>\n</body>",
);
await fs.writeFile(output, html);
console.log("Exported offline prototype: " + output);
