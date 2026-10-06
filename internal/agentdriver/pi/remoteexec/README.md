# Pi remote workspace tools

The TypeScript core in this directory was adapted from the Agentbox `remote-exec` extension by the same author. It is embedded into Wi and materialized in the user's cache when the bundled Pi driver starts against a remote workspace.

The code is distributed under Wi's MIT license. `index.ts` is the Wi-specific target resolver; the remaining modules implement tool redirection, path mapping, completion, context discovery, timeout handling, and cancellation.
