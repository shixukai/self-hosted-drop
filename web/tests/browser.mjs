import puppeteer from "puppeteer-core";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { verifyFolderUpload } from "./folder-upload.mjs";
const base = process.env.DROP_TEST_BASE || "http://localhost:18180";
assert(
  ["localhost", "127.0.0.1"].includes(new URL(base).hostname),
  "Local browser tests only",
);
const password = (
  await fs.readFile(
    process.env.DROP_TEST_PASSWORD_FILE || "../.local/admin.password",
    "utf8",
  )
).trim();
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "drop-browser-"));
const browser = await puppeteer.launch({
  executablePath:
    process.env.CHROME_EXECUTABLE_PATH ||
    (process.platform === "darwin"
      ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
      : "/usr/bin/google-chrome"),
  userDataDir: path.join(temporary, "profile"),
  headless: true,
  args: ["--no-first-run", "--no-default-browser-check"],
});
let siteID;
try {
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("response", (response) => {
    if (
      response.url().includes("/api/") &&
      response.status() >= 400 &&
      !(response.status() === 401 && response.url().endsWith("/auth/session"))
    )
      errors.push(
        "HTTP " + response.status() + " " + new URL(response.url()).pathname,
      );
  });
  await page.setViewport({ width: 1440, height: 1080, deviceScaleFactor: 1 });
  await page.goto(base, { waitUntil: "networkidle0" });
  await page.waitForSelector("input[type=password]");
  await page.type("input[type=password]", password);
  await page.click("button[type=submit]");
  await page.waitForFunction(() => location.pathname === "/");
  async function click(text) {
    await page.waitForFunction(
      (text) =>
        [...document.querySelectorAll("button")].some(
          (b) => b.textContent.trim() === text && !b.disabled,
        ),
      {},
      text,
    );
    const target = await page.evaluateHandle(
      (text) =>
        [...document.querySelectorAll("button")].find(
          (button) => button.textContent.trim() === text && !button.disabled,
        ),
      text,
    );
    await target.asElement().click();
    await target.dispose();
  }
  await click("新建站点");
  await page.waitForSelector('input[placeholder="my-report"]');
  const slug = "browser-" + Date.now().toString(36);
  await page.type('input[placeholder="例如：我的设计周报"]', "浏览器验收作品");
  await page.type('input[placeholder="my-report"]', slug);
  const html =
    '<!doctype html><meta charset="utf-8"><h1>Browser upload verified</h1>';
  const file = path.join(temporary, "browser.html");
  await fs.writeFile(file, html);
  await (await page.$("input[type=file]:not([multiple])")).uploadFile(file);
  await click("上传并检查");
  await page.waitForFunction(
    () => new URLSearchParams(location.search).has("deployment"),
    { timeout: 60000 },
  );
  siteID = await page.evaluate(() =>
    new URLSearchParams(location.search).get("site"),
  );
  await click("确认公开发布");
  await page.waitForSelector(".success-panel", { timeout: 60000 });
  const url = await page.$eval(".success-panel a", (a) => a.href);
  const publicPage = await browser.newPage();
  await publicPage.goto(url);
  assert.equal(
    await publicPage.$eval("h1", (e) => e.textContent),
    "Browser upload verified",
  );
  await publicPage.close();
  await click("查看站点");
  await page.waitForSelector(".release-row");
  await click("受控预览");
  await page.waitForSelector("[role=dialog] a");
  const previewURL = await page.$eval("[role=dialog] a", (a) => a.href);
  const preview = await browser.newPage();
  await preview.goto(previewURL);
  await preview.waitForFunction(
    () => location.pathname === "/" && !location.hash,
    { timeout: 30000 },
  );
  assert.equal(
    await preview.$eval("h1", (e) => e.textContent),
    "Browser upload verified",
  );
  await preview.close();
  await page.keyboard.press("Escape");
  await click("暂停");
  await page.waitForSelector("[role=dialog]");
  await click("确认");
  await page.waitForSelector('[role="dialog"]', { hidden: true });
  await page.waitForFunction(
    () => document.body.textContent.includes("恢复服务"),
    { timeout: 30000 },
  );
  await click("恢复服务");
  await click("确认");
  await page.waitForSelector('[role="dialog"]', { hidden: true });
  await page.waitForFunction(
    () =>
      [...document.querySelectorAll("button")].some(
        (b) => b.textContent.trim() === "暂停",
      ),
    { timeout: 30000 },
  );
  await verifyFolderUpload(page, browser, temporary, base, click);
  for (const route of ["/", "/deployments", "/tokens", "/system", "/upload"]) {
    await page.goto(base + route, { waitUntil: "networkidle0" });
    assert.equal(
      await page.$eval("body", (e) => e.scrollWidth),
      1440,
      "Desktop overflow on " + route,
    );
  }
  await page.click('button[aria-label="切换深色主题"]');
  assert.equal(await page.$eval("html", (e) => e.className), "dark");
  await page.setViewport({ width: 390, height: 844, deviceScaleFactor: 1 });
  await page.goto(base, { waitUntil: "networkidle0" });
  assert.equal(
    await page.$eval("body", (e) => e.scrollWidth),
    390,
    "Mobile horizontal overflow",
  );
  await page.click('button[aria-label="切换浅色主题"]');
  if (process.env.DROP_SCREENSHOT_DIR) {
    await fs.mkdir(process.env.DROP_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({
      path: path.join(process.env.DROP_SCREENSHOT_DIR, "mobile.png"),
      fullPage: true,
    });
    await page.setViewport({ width: 1440, height: 1080, deviceScaleFactor: 1 });
    await page.screenshot({
      path: path.join(process.env.DROP_SCREENSHOT_DIR, "desktop.png"),
      fullPage: true,
    });
  }
  assert.deepEqual(errors, []);
  // Remove only this test's site, retaining its history in the test stack's trash.
  const publication = await page.evaluate(async (siteID) => {
    const s = await (await fetch("/api/v1/auth/session")).json();
    const site = await (await fetch("/api/v1/sites/" + siteID)).json();
    return (
      await fetch("/api/v1/sites/" + siteID, {
        method: "DELETE",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": s.csrfToken,
        },
        body: JSON.stringify({
          expectedGeneration: site.generation,
          confirmSlug: site.slug,
        }),
      })
    ).json();
  }, siteID);
  assert(publication.id);
  console.log(
    JSON.stringify({
      passed: [
        "UI login",
        "HTML upload and publish",
        "public content",
        "preview fragment exchange",
        "pause and resume dialogs",
        "native folder drop with 108 files and nested assets",
        "directory picker with one nested file",
        "empty folder and required-field feedback",
        "all routes",
        "dark/light theme",
        "390px mobile layout",
        "no runtime errors",
      ],
    }),
  );
} finally {
  await browser.close();
  await fs.rm(temporary, { recursive: true, force: true });
}
