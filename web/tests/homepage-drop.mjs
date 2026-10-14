import fs from "node:fs/promises";
import path from "node:path";
import { createHash } from "node:crypto";
import assert from "node:assert/strict";
import { zipSync, strToU8 } from "fflate";
import { waitForCleanupPublication } from "./folder-upload.mjs";

// CDP supplies real operating-system files. In particular, directory entries
// and Chromium's readEntries batches must not be replaced with JavaScript mocks.
export async function verifyHomepageDrop(page, browser, temporary, base, click) {
  const cdp = await page.createCDPSession();
  const unique = Date.now().toString(36);
  let siteID;

  async function home() {
    await page.goto(base, { waitUntil: "networkidle0" });
    await page.waitForSelector(".drop-launch");
  }

  async function drop(filePath, target = ".drop-launch") {
    await page.$eval(target, (el) => el.scrollIntoView({ block: "center" }));
    const box = await (await page.$(target)).boundingBox();
    assert(box, "Homepage drop target must be visible");
    const event = {
      x: box.x + box.width / 2,
      y: box.y + box.height / 2,
      data: { items: [], files: [filePath], dragOperationsMask: 1 },
    };
    for (const type of ["dragEnter", "dragOver"])
      await cdp.send("Input.dispatchDragEvent", { type, ...event });
    await page.waitForSelector(".drop-launch.dragging");
    await cdp.send("Input.dispatchDragEvent", { type: "drop", ...event });
  }

  async function selected(label, count) {
    await page.waitForFunction(() => location.pathname === "/upload");
    await page.waitForSelector(".file-selection");
    assert.equal(
      await page.$eval(".file-selection strong", (el) => el.textContent),
      label,
      "Homepage navigation must retain the actual dropped files",
    );
    assert.match(
      await page.$eval(".file-selection p", (el) => el.textContent),
      new RegExp(`^${count} 个文件`),
    );
    assert.equal(
      await page.$eval('input[placeholder="my-report"]', (el) => el.value),
      label.replace(/\.(html?|zip)$/i, ""),
      "Dropped file or folder should suggest the site slug",
    );
  }

  async function ready(expectedFiles, expectedFilename) {
    await click("上传并检查");
    await page.waitForFunction(
      () => new URLSearchParams(location.search).has("site"),
      { timeout: 60000 },
    );
    siteID = await page.evaluate(() =>
      new URLSearchParams(location.search).get("site"),
    );
    await page.waitForFunction(
      () => document.body.textContent.includes("检查完成，再确认一下。"),
      { timeout: 60000 },
    );
    const deployment = await page.evaluate(async () => {
      const id = new URLSearchParams(location.search).get("deployment");
      const response = await fetch("/api/v1/deployments/" + id);
      if (!response.ok)
        throw new Error("Homepage deployment check failed: " + response.status);
      return response.json();
    });
    assert.equal(deployment.status, "READY");
    assert.equal(deployment.filename, expectedFilename);
    assert.equal(deployment.fileCount, Object.keys(expectedFiles).length);
    assert.deepEqual(
      deployment.files.map((file) => file.path).sort(),
      Object.keys(expectedFiles).sort(),
      "Nested paths must survive the homepage handoff and ZIP packing",
    );
    for (const file of deployment.files) {
      const expected = Buffer.from(expectedFiles[file.path]);
      assert.equal(file.size, expected.length, file.path + " size");
      assert.equal(
        file.sha256,
        createHash("sha256").update(expected).digest("hex"),
        file.path + " content",
      );
    }
    return deployment;
  }

  async function cleanup() {
    if (!siteID) return;
    const publication = await page.evaluate(async (id) => {
      const session = await (await fetch("/api/v1/auth/session")).json();
      const site = await (await fetch("/api/v1/sites/" + id)).json();
      const response = await fetch("/api/v1/sites/" + id, {
        method: "DELETE",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": session.csrfToken,
        },
        body: JSON.stringify({
          expectedGeneration: site.generation,
          confirmSlug: site.slug,
        }),
      });
      if (!response.ok)
        throw new Error("Homepage test cleanup failed: " + response.status);
      return response.json();
    }, siteID);
    await waitForCleanupPublication(page, publication, "Homepage test cleanup");
    siteID = undefined;
  }

  async function readingDoesNotOverrideLazyNavigation() {
    const folder = path.join(temporary, "home-navigation-" + unique);
    await fs.mkdir(folder);
    await fs.writeFile(path.join(folder, "index.html"), "<h1>Navigation test</h1>");
    // Real filesystem callbacks keep a read pending long enough to click the
    // destination. The lazy JavaScript request is held, not the directory API.
    for (let i = 0; i < 1500; i++)
      await fs.writeFile(path.join(folder, `asset-${i}.txt`), `navigation ${i}`);

    let heldRequest;
    let timeout;
    let resolveSystemRequest;
    const systemRequest = new Promise((resolve) => {
      resolveSystemRequest = resolve;
    });
    const requestErrors = [];
    const onRequest = async (request) => {
      if (request.isInterceptResolutionHandled()) return;
      const url = new URL(request.url());
      if (
        !heldRequest &&
        url.origin === new URL(base).origin &&
        request.resourceType() === "script" &&
        /\/SystemPage(?:-[^/]*)?\.(?:js|tsx)$/.test(url.pathname)
      ) {
        heldRequest = request;
        resolveSystemRequest();
        return;
      }
      try {
        await request.continue();
      } catch (error) {
        requestErrors.push(error.message);
      }
    };
    await page.setCacheEnabled(false);
    await page.setRequestInterception(true);
    page.on("request", onRequest);
    try {
      // Reload gives SystemPage a fresh module registry even if another test
      // previously visited it. Browser cache cannot bypass the interception.
      await home();
      await drop(folder);
      await page.waitForSelector('.drop-launch[aria-busy="true"]');
      await click("系统");
      await Promise.race([
        systemRequest,
        new Promise((_, reject) => {
          timeout = setTimeout(
            () => reject(new Error("SystemPage lazy request was not intercepted")),
            30000,
          );
        }),
      ]);
      await page.waitForFunction(() => location.pathname === "/system");
      assert(
        await page.$(".drop-launch"),
        "Suspense must retain the old homepage while SystemPage is loading",
      );
      await page.waitForFunction(
        () =>
          location.pathname !== "/system" ||
          document.querySelector(".drop-launch")?.getAttribute("aria-busy") ===
            "false",
        { timeout: 60000 },
      );
      assert.equal(
        new URL(page.url()).pathname,
        "/system",
        "Completing an old homepage read must not navigate away from SystemPage",
      );
      assert.equal(await page.$(".file-selection"), null);
      await heldRequest.continue();
      heldRequest = undefined;
      await page.waitForFunction(() =>
        document.querySelector("h1")?.textContent.includes("所有状态"),
      );
      assert.equal(new URL(page.url()).pathname, "/system");
      assert.deepEqual(requestErrors, []);
    } finally {
      clearTimeout(timeout);
      try {
        if (heldRequest && !heldRequest.isInterceptResolutionHandled())
          await heldRequest.continue();
      } finally {
        await page.setRequestInterception(false);
        page.off("request", onRequest);
        await page.setCacheEnabled(true);
      }
    }
  }

  try {
    await readingDoesNotOverrideLazyNavigation();
    const empty = path.join(temporary, "homepage-empty-" + unique);
    await fs.mkdir(empty);
    const unsupported = path.join(temporary, "homepage-unsupported.txt");
    await fs.writeFile(unsupported, "A loose text file is not a website");
    for (const [file, message] of [
      [empty, "没有找到"],
      [unsupported, "HTML 或 ZIP"],
    ]) {
      await home();
      await drop(file);
      await page.waitForFunction(
        (message) =>
          document
            .querySelector('[role="alert"]')
            ?.textContent.includes(message),
        {},
        message,
      );
      assert.equal(new URL(page.url()).pathname, "/");
      assert.equal(await page.$(".file-selection"), null);
      assert.equal(await page.$(".drop-launch.dragging"), null);
    }

    const htmlName = "home-html-" + unique + ".html";
    const htmlFile = path.join(temporary, htmlName);
    const html = '<!doctype html><meta charset="utf-8"><h1>Homepage HTML drop</h1>';
    await fs.writeFile(htmlFile, html);
    await home();
    await drop(htmlFile);
    await selected(htmlName, 1);

    // Files handed across a route transition are consumed once. An ordinary
    // create flow must not silently replay an earlier browser-history draft.
    await page.goBack({ waitUntil: "networkidle0" });
    await page.waitForFunction(() => location.pathname === "/");
    await page.waitForSelector(".drop-launch");
    // History restores the banner's previous scroll position. Keep the main
    // create button outside the sticky header before Puppeteer's pointer click.
    await page.evaluate(() =>
      window.scrollTo({ top: 0, left: 0, behavior: "instant" }),
    );
    await page.waitForFunction(() => {
      // Native history restoration may complete after the first scroll call.
      window.scrollTo({ top: 0, left: 0, behavior: "instant" });
      const button = [...document.querySelectorAll("button")].find(
        (el) => el.textContent.trim() === "新建站点" && !el.disabled,
      );
      const header = document.querySelector(".app-header");
      if (!button || !header) return false;
      const box = button.getBoundingClientRect();
      const headerBox = header.getBoundingClientRect();
      return (
        box.top >= headerBox.bottom &&
        box.bottom <= window.innerHeight &&
        button.contains(
          document.elementFromPoint(
            box.left + box.width / 2,
            box.top + box.height / 2,
          ),
        )
      );
    });
    await click("新建站点");
    await page.waitForFunction(() => location.pathname === "/upload");
    await page.waitForSelector('input[placeholder="my-report"]');
    assert.equal(await page.$(".file-selection"), null);
    assert.equal(
      await page.$eval('input[placeholder="my-report"]', (el) => el.value),
      "",
    );
    await home();
    await drop(htmlFile);
    await selected(htmlName, 1);
    await ready({ [htmlName]: html }, htmlName);
    await cleanup();

    const zipName = "home-zip-" + unique + ".zip";
    const zipFile = path.join(temporary, zipName);
    const zipFiles = {
      "index.html": '<!doctype html><meta charset="utf-8"><h1>Homepage ZIP drop</h1>',
      "nested/a/theme.css": "h1{color:navy}",
      "nested/b/theme.css": "body{margin:17px}",
    };
    await fs.writeFile(
      zipFile,
      zipSync(
        Object.fromEntries(
          Object.entries(zipFiles).map(([name, content]) => [
            name,
            strToU8(content),
          ]),
        ),
      ),
    );
    await home();
    // The whole sites section accepts drops, including outside the banner.
    await drop(zipFile, "#sites-title");
    await selected(zipName, 1);
    await ready(zipFiles, zipName);
    await cleanup();

    const folderName = "home-folder-" + unique;
    const folder = path.join(temporary, folderName);
    const folderFiles = {
      "index.html": '<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="styles/a/theme.css"><link rel="stylesheet" href="styles/b/theme.css"><h1>Homepage directory drop verified</h1>',
      "styles/a/theme.css": "h1{color:rgb(19, 38, 57)}",
      "styles/b/theme.css": "body{margin:23px}",
    };
    for (let i = 0; i < 105; i++)
      folderFiles[`assets/file-${i}.txt`] = `homepage asset ${i}`;
    for (const [name, content] of Object.entries(folderFiles)) {
      const file = path.join(folder, name);
      await fs.mkdir(path.dirname(file), { recursive: true });
      await fs.writeFile(file, content);
    }
    await home();
    await drop(folder);
    await selected(folderName, 108);
    const deployment = await ready(folderFiles, "website.zip");
    assert.equal(deployment.report.entryPath, "index.html");
    await click("确认公开发布");
    await page.waitForSelector(".success-panel", { timeout: 60000 });
    const url = await page.$eval(".success-panel a", (el) => el.href);
    const publicPage = await browser.newPage();
    try {
      await publicPage.setCacheEnabled(false);
      const response = await publicPage.goto(url);
      assert.equal(response.status(), 200);
      assert.equal(
        await publicPage.$eval("h1", (el) => el.textContent),
        "Homepage directory drop verified",
      );
      assert.equal(
        await publicPage.$eval("h1", (el) => getComputedStyle(el).color),
        "rgb(19, 38, 57)",
        "The nested stylesheet must load in the published site",
      );
      for (const resource of [
        "styles/a/theme.css",
        "styles/b/theme.css",
        "assets/file-104.txt",
      ]) {
        const resourceResponse = await publicPage.goto(
          new URL(resource, url).href,
        );
        assert.equal(resourceResponse.status(), 200, resource);
        assert.equal(
          await resourceResponse.text(),
          folderFiles[resource],
          resource,
        );
      }
    } finally {
      await publicPage.close();
    }
    await cleanup();
  } finally {
    try {
      await cleanup();
    } finally {
      await cdp.detach();
    }
  }
}
