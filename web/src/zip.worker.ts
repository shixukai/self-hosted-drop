import { Zip, AsyncZipDeflate } from "fflate";
self.onmessage = async (event: MessageEvent<{ files: File[] }>) => {
  const files = event.data.files;
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  const zip = new Zip((error, data, final) => {
    if (error) {
      self.postMessage({ error: error.message });
      return;
    }
    bytes += data.length;
    if (bytes > 200 * 1024 * 1024) {
      zip.terminate();
      self.postMessage({ error: "目录打包后超过 200 MiB，请拆分后上传" });
      return;
    }
    chunks.push(data);
    if (final)
      self.postMessage({
        blob: new Blob(chunks as BlobPart[], { type: "application/zip" }),
      });
  });
  try {
    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      const path = file.webkitRelativePath || file.name;
      const entry = new AsyncZipDeflate(path, { level: 3 });
      zip.add(entry);
      const reader = file.stream().getReader();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) {
          entry.push(new Uint8Array(), true);
          break;
        }
        entry.push(value, false);
      }
      self.postMessage({
        progress: Math.round(((i + 1) / files.length) * 100),
      });
    }
    zip.end();
  } catch (e) {
    zip.terminate();
    self.postMessage({
      error: e instanceof Error ? e.message : "目录打包失败",
    });
  }
};
