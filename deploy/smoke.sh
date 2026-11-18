#!/bin/sh
set -eu

CDPATH='' cd -- "$(dirname "$0")/.."
umask 077
hub_root=$(mktemp -d "${TMPDIR:-/tmp}/openplexer-smoke.XXXXXX")
hub_pid=
cleanup() {
	if [ -n "$hub_pid" ]; then
		kill "$hub_pid" 2>/dev/null || :
		wait "$hub_pid" 2>/dev/null || :
	fi
	rm -rf "$hub_root"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$hub_root/bin"
go build -o "$hub_root/bin/" ./apps/...

hub="$hub_root/bin/openplexer-hub"
data_dir="$hub_root/data"
mkdir -m 700 "$data_dir"
"$hub" init -data-dir "$data_dir"

"$hub" add-member -data-dir "$data_dir" -id mem-lee -role lead > "$hub_root/mem-lee.token"
"$hub" add-member -data-dir "$data_dir" -id mem-ana > "$hub_root/mem-ana.token"
"$hub" add-member -data-dir "$data_dir" -id mem-sam > "$hub_root/mem-sam.token"

project=prj-shop
repo=api
"$hub" add-project -data-dir "$data_dir" -id "$project" -repo "$repo"
jq --arg project "$project" --arg repo "$repo" \
	'.project_id = $project | .repos = [$repo]' \
	spec/policy/v1/examples/bundle/minimal.json > "$hub_root/bundle.json"
"$hub" publish-policy -data-dir "$data_dir" -project "$project" -file "$hub_root/bundle.json"

listen=127.0.0.1:8787
hub_url="http://$listen"
"$hub" serve -data-dir "$data_dir" -listen "$listen" > "$hub_root/hub.log" 2>&1 &
hub_pid=$!

attempts=0
until curl --noproxy 127.0.0.1 --fail --silent --show-error --max-time 2 \
	"$hub_url/.well-known/jwks.json" -o "$hub_root/keys.json" 2> "$hub_root/curl.log"; do
	attempts=$((attempts + 1))
	if ! kill -0 "$hub_pid" 2>/dev/null || [ "$attempts" -ge 10 ]; then
		cat "$hub_root/hub.log" >&2
		if grep -Eiq 'bind: (operation not permitted|permission denied)' "$hub_root/hub.log"; then
			echo "Smoke skipped: the OS denied binding $listen (check sandbox restrictions)." >&2
			exit 77
		fi
		cat "$hub_root/curl.log" >&2
		echo "Smoke failed: the hub did not become ready at $hub_url." >&2
		exit 1
	fi
	sleep 1
done

for member in mem-lee mem-ana mem-sam; do
	printf 'Authorization: Bearer %s\n' "$(cat "$hub_root/$member.token")" |
		curl --noproxy 127.0.0.1 --fail --silent --show-error --max-time 2 --header @- \
			"$hub_url/api/v1/projects/$project/policy.bundle" -o "$hub_root/$member.bundle.json"

	OPENPLEXER_HOME="$hub_root/check-$member" "$hub_root/bin/openplexer" \
		configure --keys "$hub_root/keys.json" --member "$member"
	OPENPLEXER_HOME="$hub_root/check-$member" "$hub_root/bin/openplexer" \
		policy load "$hub_root/$member.bundle.json"
done

kill "$hub_pid"
wait "$hub_pid" 2>/dev/null || :
hub_pid=
echo 'Smoke passed. All three members fetched and verified the signed policy.'
