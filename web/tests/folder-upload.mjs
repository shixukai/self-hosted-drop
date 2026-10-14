import fs from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";

export async function waitForCleanupPublication(page, publication, label) {
  assert(publication.id, label + " returned no publication ID");
  const id = publication.id;
  const deadline = Date.now() + 60000;
  while (publication.status !== "APPLIED") {
    if (["FAILED", "CONFLICT", "CANCELED"].includes(publication.status))
      throw new Error(
        `${label} ${id}: ${publication.status} ${publication.errorCode || ""} ${publication.errorMessage || ""}`,
      );
    if (Date.now() >= deadline)
      throw new Error(`${label} ${id} timed out: ${publication.status}`);
    await new Promise((resolve) => setTimeout(resolve, 1000));
    publication = await page.evaluate(
      async ({ id, label }) => {
        const response = await fetch(
          "/api/v1/publications/" + encodeURIComponent(id),
        );
        if (!response.ok)
          throw new Error(`${label} ${id} status check failed: ${response.status}`);
        return response.json();
      },
      { id, label },
    );
  }
}

export async function verifyFolderUpload(
  page,
  browser,
  temporary,
  base,
  click,
) {
  const cdp = await page.createCDPSession();
  let siteID;
  async function drop(filePath) {
    const box = await (await page.$(".dropzone")).boundingBox();
    const data = { items: [], files: [filePath], dragOperationsMask: 1 };
    for (const type of ["dragEnter", "dragOver", "drop"])
      await cdp.send("Input.dispatchDragEvent", {
        type,
        x: box.x + box.width / 2,
        y: box.y + box.height / 2,
        data,
      });
  }
  async function ready() {
    await page.waitForFunction(
      () => new URLSearchParams(location.search).has("deployment"),
      { timeout: 60000 },
    );
    siteID = await page.evaluate(() =>
      new URLSearchParams(location.search).get("site"),
    );
    await page.waitForFunction(
      () => document.body.textContent.includes("检查完成，再确认一下。"),
      { timeout: 60000 },
    );
    return page.evaluate(async () => {
      const id = new URLSearchParams(location.search).get("deployment");
      return (await fetch("/api/v1/deployments/" + id)).json();
    });
  }
  try {
    await page.goto(base + "/upload", { waitUntil: "networkidle0" });
    assert.match(
      await page.$eval("#upload-requirement", (el) => el.textContent),
      /先添加/,
    );
    const empty = path.join(temporary, "empty-directory");
    await fs.mkdir(empty);
    await drop(empty);
    await page.waitForFunction(() =>
      document
        .querySelector('[role="alert"]')
        ?.textContent.includes("没有找到"),
    );
    assert.equal(await page.$(".file-selection"), null);

    const slug = "folder-" + Date.now().toString(36);
    const folder = path.join(temporary, slug);
    for (const dir of ["styles/a", "styles/b", "assets"])
      await fs.mkdir(path.join(folder, dir), { recursive: true });
    await fs.writeFile(
      path.join(folder, "index.html"),
      '<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="styles/a/theme.css"><link rel="stylesheet" href="styles/b/theme.css"><h1>Directory drag verified</h1>',
    );
    await fs.writeFile(
      path.join(folder, "styles/a/theme.css"),
      "h1{color:red}",
    );
    await fs.writeFile(
      path.join(folder, "styles/b/theme.css"),
      "body{margin:24px}",
    );
    for (let i = 0; i < 105; i++)
      await fs.writeFile(
        path.join(folder, `assets/file-${i}.txt`),
        `asset ${i}`,
      );
    await drop(folder);
    await page.waitForFunction(() =>
      document
        .querySelector(".file-selection")
        ?.textContent.includes("108 个文件"),
    );
    assert.equal(
      await page.$eval(
        'input[placeholder="例如：我的设计周报"]',
        (e) => e.value,
      ),
      slug,
    );
    assert.equal(
      await page.$eval('input[placeholder="my-report"]', (e) => e.value),
      slug,
    );
    // Clearing required information explains the disabled button.
    await page.$eval('input[placeholder="my-report"]', (el) => el.select());
    await page.keyboard.press("Backspace");
    await page.waitForFunction(() =>
      document
        .querySelector("#upload-requirement")
        ?.textContent.includes("请填写站点标识"),
    );
    await page.type('input[placeholder="my-report"]', slug);
    await click("上传并检查");
    const deployment = await ready();
    assert.equal(deployment.fileCount, 108);
    assert(deployment.files.some((file) => file.path === "styles/a/theme.css"));
    assert(deployment.files.some((file) => file.path === "styles/b/theme.css"));
    assert(
      deployment.files.some((file) => file.path === "assets/file-104.txt"),
    );
    if (process.env.DROP_SCREENSHOT_DIR) {
      await fs.mkdir(process.env.DROP_SCREENSHOT_DIR, { recursive: true });
      await page.screenshot({
        path: path.join(
          process.env.DROP_SCREENSHOT_DIR,
          "folder-upload-ready.png",
        ),
        fullPage: true,
      });
    }
    await click("确认公开发布");
    await page.waitForSelector(".success-panel", { timeout: 60000 });
    const url = await page.$eval(".success-panel a", (a) => a.href);
    const publicPage = await browser.newPage();
    try {
      await publicPage.setCacheEnabled(false);
      await publicPage.goto(url);
      assert.equal(
        await publicPage.$eval("h1", (e) => e.textContent),
        "Directory drag verified",
      );
      for (const [resource, content] of [
        ["styles/a/theme.css", "h1{color:red}"],
        ["styles/b/theme.css", "body{margin:24px}"],
        ["assets/file-104.txt", "asset 104"],
      ]) {
        const response = await publicPage.goto(new URL(resource, url).href);
        assert.equal(response.status(), 200);
        assert.equal(await response.text(), content);
      }
    } finally {
      await publicPage.close();
    }

    // A single-file directory still goes through ZIP packing, preserving paths.
    const single = path.join(temporary, "single-file-site");
    await fs.mkdir(path.join(single, "nested"), { recursive: true });
    await fs.writeFile(
      path.join(single, "nested/index.html"),
      "<h1>Single file directory</h1>",
    );
    await page.goto(base + "/upload?site=" + siteID, {
      waitUntil: "networkidle0",
    });
    await (await page.$("input[webkitdirectory]")).uploadFile(single);
    await page.waitForSelector(".file-selection");
    assert.equal(
      await page.$eval('input[placeholder="my-report"]', (e) => e.value),
      slug,
    );
    await click("上传并检查");
    const singleDeployment = await ready();
    assert.equal(singleDeployment.fileCount, 1);
    assert.equal(singleDeployment.report.entryPath, "index.html");
    assert.equal(singleDeployment.filename, "website.zip");
  } finally {
    await cdp.detach();
    if (siteID) {
      // Only this test's generated site is moved into the recoverable trash.
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
          throw new Error("Folder test cleanup failed: " + response.status);
        return response.json();
      }, siteID);
      await waitForCleanupPublication(page, publication, "Folder test cleanup");
    }
  }
}
