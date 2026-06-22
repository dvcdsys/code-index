// Format a server-reported version string for display.
//
// The version already carries its leading "v" when the binary is built from a
// release tag (server/vX.Y.Z → ldflags stamps "vX.Y.Z"), but the dev
// placeholder ("0.0.0-dev") does not. The UI used to hard-prefix "v", which
// rendered "vv0.11.0" for tagged builds. Normalize to exactly one leading "v"
// so it reads cleanly no matter how the build stamped it.
export function formatVersion(version: string | null | undefined): string {
  const trimmed = (version ?? '').trim();
  if (!trimmed) return '';
  return `v${trimmed.replace(/^v+/i, '')}`;
}
