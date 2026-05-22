package agent

import (
	"os"
	"path/filepath"
)

const PiAttachmentExtensionEnv = "AGENTBRIDGE_PI_ATTACHMENT_EXTENSION"

const piAttachmentExtensionSource = `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { readFile } from "node:fs/promises";

type Attachment = {
  id: string;
  kind: string;
  file_name: string;
  mime_type: string;
  size: number;
  path?: string;
  extracted_text?: string;
};

async function loadAttachments(): Promise<Attachment[]> {
  const manifest = process.env.AGENTBRIDGE_ATTACHMENT_MANIFEST;
  if (!manifest) return [];
  try {
    const raw = await readFile(manifest, "utf8");
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed.attachments) ? parsed.attachments : [];
  } catch {
    return [];
  }
}

function matchAttachment(items: Attachment[], query: string): Attachment | undefined {
  const q = query.toLowerCase();
  return items.find(a => a.id.toLowerCase() === q)
    ?? items.find(a => a.file_name.toLowerCase() === q)
    ?? items.find(a => a.file_name.toLowerCase().includes(q));
}

export default function(pi: ExtensionAPI) {
  pi.registerTool({
    name: "attachment_read",
    label: "Read attachment",
    description: "Read an AgentBridge-uploaded attachment by id or file name. Returns metadata, local path, and extracted text when available.",
    promptSnippet: "Read AgentBridge uploaded attachments by id or file name",
    promptGuidelines: [
      "Use attachment_read when the user references an uploaded or attached file before searching the filesystem.",
      "Use the path returned by attachment_read directly if a built-in file tool is needed. Do not search unrelated directories for uploaded attachments."
    ],
    parameters: Type.Object({
      query: Type.String({ description: "Attachment id or file name. Use list=true to list available attachments." }),
      list: Type.Optional(Type.Boolean({ description: "List available attachments instead of reading one." }))
    }),
    async execute(_toolCallId, params) {
      const attachments = await loadAttachments();
      if (params.list) {
        const text = attachments.length
          ? attachments.map(a => a.id + "\t" + a.file_name + "\t" + a.mime_type + "\t" + a.size + " bytes" + (a.path ? "\t" + a.path : "")).join("\n")
          : "No AgentBridge attachments are available for this session.";
        return { content: [{ type: "text", text }], details: { attachments } };
      }
      const att = matchAttachment(attachments, params.query);
      if (!att) throw new Error("Attachment not found: " + params.query);
      let text = "Attachment: " + att.file_name + "\nID: " + att.id + "\nType: " + att.kind + "\nMIME: " + att.mime_type + "\nSize: " + att.size + " bytes";
      if (att.path) text += "\nPath: " + att.path;
      if (att.extracted_text) text += "\n\nExtracted text:\n" + att.extracted_text;
      else if (att.path) text += "\n\nNo extracted text is available. Use the returned path directly with read/bash if needed.";
      return { content: [{ type: "text", text }], details: { attachment: att } };
    }
  });
}
`

func EnsurePiAttachmentExtension(path string) (string, error) {
	if path == "" {
		path = defaultPiAttachmentExtensionPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(piAttachmentExtensionSource), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func defaultPiAttachmentExtensionPath() string {
	if dir := os.Getenv("AGENTBRIDGE_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "pi-attachments-extension.ts")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "agentbridge", "pi-attachments-extension.ts")
	}
	return filepath.Join(".", ".agentbridge-pi-attachments-extension.ts")
}
