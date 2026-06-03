// Derive a cix CLI server alias from the browser host. The CLI stores each
// server as one entry under `server.<alias>` and parses that config key by
// splitting on dots, so the alias must be dot-free (and whitespace-free, and
// non-empty) — see validateServerName / parseServerKey in the CLI. We fold the
// host (including any non-default port, so distinct ports get distinct aliases)
// to [a-z0-9-]:  "cix.example.com" -> "cix-example-com",
// "localhost:21847" -> "localhost-21847".
//
// Shared by the API-key "Connect the cix CLI" popup and the home-page
// onboarding card so the two never drift.
export function cixServerAlias(host: string): string {
  const alias = host
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return alias || 'default';
}

// Build the one-paste command that registers a server (URL + key as a single
// `server.<alias>` entry) in the cix CLI config and makes it the default.
// Values are shell-safe (a URL, a `cix_<hex>` key, an [a-z0-9-] alias), so no
// quoting is needed — matching the CLI README's unquoted examples. `key`
// defaults to a `<key>` placeholder for previews where no secret is revealed.
export function cixConnectCommand(origin: string, host: string, key = '<key>'): string {
  const alias = cixServerAlias(host);
  return (
    `cix config set server.${alias}.url ${origin} && ` +
    `cix config set server.${alias}.key ${key} && ` +
    `cix config set default_server ${alias}`
  );
}
