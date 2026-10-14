export type UploadFile = { file: File; path: string };

export function fromFileList(files: File[]): UploadFile[] {
  return files.map((file) => ({
    file,
    path: file.webkitRelativePath || file.name,
  }));
}

export function selectionLabel(files: UploadFile[]): string {
  const root = files[0]?.path.split("/")[0];
  if (root && files.every(({ path }) => path.startsWith(root + "/")))
    return root;
  return files.length === 1 ? files[0].file.name : `${files.length} 个文件`;
}

export function validSlug(slug: string): boolean {
  return (
    /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(slug) &&
    !slug.startsWith("d-") &&
    !["api", "www", "admin", "drop"].includes(slug)
  );
}

export function suggestedSite(files: UploadFile[]) {
  const label = selectionLabel(files);
  const name = (
    files.length === 1 && !files[0].path.includes("/")
      ? label.replace(/\.(html?|zip)$/i, "")
      : label
  ).slice(0, 100);
  const candidate = name
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .slice(0, 40)
    .replace(/^-+|-+$/g, "");
  return {
    name,
    slug: validSlug(candidate)
      ? candidate
      : "site-" + crypto.randomUUID().slice(0, 8),
  };
}

export function validateFiles(files: UploadFile[]) {
  if (!files.length)
    throw new Error("没有找到可上传的文件，请选择包含 HTML 的网站目录。");
  if (files.length > 10000) throw new Error("目录最多包含 10,000 个文件。");
  const standalone = files.length === 1 && !files[0].path.includes("/");
  if (standalone && !/\.(html?|zip)$/i.test(files[0].file.name))
    throw new Error("请选择 HTML 或 ZIP 文件；上传文件夹请使用“选择目录”。");
  let total = 0;
  for (const { file, path } of files) {
    const limit = standalone && /\.zip$/i.test(file.name) ? 200 : 100;
    if (file.size > limit * 1024 * 1024)
      throw new Error(`${path} 超过单文件 ${limit} MiB 限制。`);
    total += file.size;
    if (total > 1024 * 1024 * 1024)
      throw new Error("目录文件总大小不能超过 1 GiB。");
  }
}

// Capture entries synchronously during drop: DataTransfer is protected once
// the event handler returns. Keep paths explicit across the Worker boundary.
export async function readDroppedFiles(
  data: DataTransfer,
  signal: AbortSignal,
  onProgress: (count: number) => void,
): Promise<UploadFile[]> {
  const items = Array.from(data.items)
    .filter((item) => item.kind === "file")
    .map((item) => ({
      entry: item.webkitGetAsEntry?.(),
      file: item.getAsFile(),
    }));
  const fallback = fromFileList(Array.from(data.files));
  const files: UploadFile[] = [];
  let total = 0;
  let entries = 0;
  const checkCanceled = () => {
    if (signal.aborted) throw new Error("目录读取已取消，可以重新选择文件。");
  };
  async function walk(entry: FileSystemEntry, parent = "", depth = 0) {
    checkCanceled();
    const path = parent + entry.name;
    if (depth >= 20) throw new Error("目录层级不能超过 20 层。");
    if (++entries > 20000) throw new Error("目录包含过多文件或子目录。");
    if (entry.isDirectory) {
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      // Chromium returns directories in batches (often 100 entries).
      for (;;) {
        checkCanceled();
        const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
          reader.readEntries(resolve, reject),
        );
        if (!batch.length) break;
        for (const child of batch) await walk(child, path + "/", depth + 1);
      }
    } else if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) =>
        (entry as FileSystemFileEntry).file(resolve, reject),
      );
      checkCanceled();
      if (files.length >= 10000)
        throw new Error("目录最多包含 10,000 个文件。");
      if (parent && file.size > 100 * 1024 * 1024)
        throw new Error(`${path} 超过单文件 100 MiB 限制。`);
      total += file.size;
      if (total > 1024 * 1024 * 1024)
        throw new Error("目录文件总大小不能超过 1 GiB。");
      files.push({ file, path });
      if (files.length === 1 || files.length % 50 === 0)
        onProgress(files.length);
    }
  }
  try {
    if (!items.length) files.push(...fallback);
    for (const item of items) {
      if (item.entry) await walk(item.entry);
      else if (item.file) files.push(...fromFileList([item.file]));
    }
    checkCanceled();
    validateFiles(files);
    return files;
  } catch (error) {
    if (error instanceof DOMException)
      throw new Error(
        "无法读取目录中的文件，请检查访问权限，或改用“选择目录”。",
      );
    throw error;
  }
}
