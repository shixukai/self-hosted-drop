import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

export async function collectNotices() {
  const root = fileURLToPath(new URL("../", import.meta.url));
  const lock = JSON.parse(
    await fs.readFile(path.join(root, "package-lock.json"), "utf8"),
  );
  const groups = new Map();
  const missing = [];

  for (const [location, entry] of Object.entries(lock.packages)) {
    if (!location || entry.dev) continue;
    const directory = path.join(root, location);
    const info = JSON.parse(
      await fs.readFile(path.join(directory, "package.json"), "utf8"),
    );
    const name = `${info.name}@${info.version}`;
    const files = (await fs.readdir(directory))
      .filter((file) => /^(licen[sc]e|notice)([.-]|$)/i.test(file))
      .sort();

    if (!files.length)
      missing.push(
        `${name}: package metadata license ${info.license || "unspecified"}`,
      );
    for (const file of files) {
      const content = (
        await fs.readFile(path.join(directory, file), "utf8")
      ).trim();
      const group = groups.get(content) || [];
      if (!group.includes(name)) group.push(name);
      groups.set(content, group);
    }
  }

  const blocks = [
    "Self-hosted Drop — third-party notices",
    "Generated from the pinned direct and transitive runtime packages. Unused package code may be omitted from the browser bundle. License text below is reproduced from the installed packages, preserving their wording.",
  ];
  for (const [content, names] of groups) {
    blocks.push(names.join("\n") + "\n\n" + content);
  }
  if (missing.length)
    blocks.push("Packages without a root license file:\n" + missing.join("\n"));
  return blocks.join("\n\n========================================\n\n") + "\n";
}

await fs.mkdir(new URL("../public/",import.meta.url),{recursive:true});
await fs.writeFile(new URL("../public/third-party-notices.txt",import.meta.url),await collectNotices());
