// Windshift coding-agent guard extension (WI-86 / WI-83).
//
// Loaded automatically by pi-coding-agent from ~/.pi/agent/extensions/.
// Its job is defense-in-depth around the container's mount + egress
// boundaries — the container can already only see /workspace + the LLM
// provider + the SCM remote, but a guard that fails fast with a clear
// reason saves a round-trip and surfaces intent in the run event stream.
//
// Phase 6 (WI-89) extends this with a session_start prompt addendum once
// the RPC integration settles which extension hook to use.

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const WORKSPACE_ROOT = "/workspace";

// Patterns the guard refuses to forward to bash. Not exhaustive — the
// container sandbox is the actual wall — but they catch the obvious
// "let's nuke the host" footguns.
const DANGEROUS_BASH_PATTERNS: RegExp[] = [
	/\brm\s+-rf\s+\/(?:\s|$)/,
	/:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:/, // classic fork bomb
	/\bchmod\s+(?:-R\s+)?777\s+\//,
	/\bcurl\b[^|]*\|\s*(?:sh|bash)\b/,
	/\bwget\b[^|]*\|\s*(?:sh|bash)\b/,
	/\bsudo\b/,
];

function isPathInsideWorkspace(value: unknown): boolean {
	if (typeof value !== "string" || value.length === 0) return true;
	// Relative paths resolve against the agent's cwd, which the
	// orchestrator sets to /workspace inside the container.
	if (!value.startsWith("/")) return true;
	return value === WORKSPACE_ROOT || value.startsWith(`${WORKSPACE_ROOT}/`);
}

function rejectIfDangerousBash(
	command: string,
): { block: true; reason: string } | undefined {
	for (const re of DANGEROUS_BASH_PATTERNS) {
		if (re.test(command)) {
			return {
				block: true,
				reason: `windshift-guard: blocked by safety policy (matched /${re.source}/)`,
			};
		}
	}
	return undefined;
}

export default function (pi: ExtensionAPI) {
	pi.on("tool_call", async (event) => {
		switch (event.toolName) {
			case "bash":
				return rejectIfDangerousBash(String(event.input?.command ?? ""));
			case "write":
			case "edit":
			case "edit-diff":
			case "read": {
				const path = event.input?.path ?? event.input?.file_path;
				if (!isPathInsideWorkspace(path)) {
					return {
						block: true,
						reason: `windshift-guard: ${event.toolName} outside ${WORKSPACE_ROOT} is not permitted (path=${String(path)})`,
					};
				}
				return undefined;
			}
			default:
				return undefined;
		}
	});
}
