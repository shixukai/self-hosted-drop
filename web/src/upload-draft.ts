import type { UploadFile } from "./upload-files";

// File objects stay in memory. Only the opaque ID travels in router state;
// consuming a draft removes it so browser history cannot replay a selection.
const drafts = new Map<
  string,
  { files: UploadFile[]; timer: ReturnType<typeof setTimeout> }
>();
const lifetime = 5 * 60 * 1000;

export function discardUploadDraft(id: string) {
  const draft = drafts.get(id);
  if (!draft) return;
  clearTimeout(draft.timer);
  drafts.delete(id);
}

export function saveUploadDraft(files: UploadFile[]): string {
  // A new homepage selection supersedes any draft that has not been opened.
  for (const id of drafts.keys()) discardUploadDraft(id);
  const id = crypto.randomUUID();
  drafts.set(id, {
    files,
    timer: setTimeout(() => discardUploadDraft(id), lifetime),
  });
  return id;
}

export function takeUploadDraft(id: string): UploadFile[] | undefined {
  const files = drafts.get(id)?.files;
  discardUploadDraft(id);
  return files;
}
