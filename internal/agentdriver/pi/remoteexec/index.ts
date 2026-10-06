import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { registerRemoteExecTools } from "./core";

export default function wiRemoteWorkspace(pi: ExtensionAPI) {
	registerRemoteExecTools(pi, {
		targetLabel: "Wi workspace",
		statusKey: "wi-workspace",
		commandName: "wi-workspace",
		commandDescription: "Show the claim-bound Wi remote workspace target",
		defaultContextFilesMode: "container",
		contextFilesFlagName: "wi-workspace-context-files",
		resolveTarget: () => {
			const runtime = process.env.WI_WORKSPACE_RUNTIME?.trim();
			const container = process.env.WI_WORKSPACE_TARGET?.trim();
			const cwd = process.env.WI_WORKSPACE_REMOTE_CWD?.trim();
			const executor = process.env.WI_WORKSPACE_EXECUTOR?.trim();
			if (!runtime || !container || !cwd || !executor) {
				throw new Error("Wi remote workspace environment is incomplete.");
			}
			return {
				runtime,
				container,
				cwd,
				statusLabel: `@${executor}: ${cwd}`,
			};
		},
	});
}
