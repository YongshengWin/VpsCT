export type SkillClient = "codex" | "claude";

function shellQuote(value: string) {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export function skillInstallCommand(origin: string, client: SkillClient) {
  const url = new URL(origin);
  const local = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if (url.username || url.password || (url.protocol !== "https:" && !(url.protocol === "http:" && local))) return null;
  const protocols = url.protocol === "https:" ? "=https" : "=http";
  const script = `set -eu; vpsct_tmp=$(mktemp); trap "rm -f \\\"$vpsct_tmp\\\"" EXIT; curl --fail --silent --show-error --connect-timeout 10 --max-time 60 --proto "${protocols}" "$1" -o "$vpsct_tmp"; vpsct_client=$2; shift 2; python3 "$vpsct_tmp" --client "$vpsct_client" "$@"`;
  return `sh -c ${shellQuote(script)} sh ${shellQuote(`${url.origin}/agent/skill/install.py`)} ${shellQuote(client)}`;
}
