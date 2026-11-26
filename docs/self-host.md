# Self-host the OpenPlexer hub

Set up one project for three people, verify its first signed policy, and keep the hub running for your team. Any hosted free-tier limits apply to the hosted service. Apache-2.0 places no team-size restriction on self-hosting. [License](../LICENSE)

## Choose a hub

Use the Go hub below if you want to run the server and keep its data and backups on infrastructure you manage. Use the Cloudflare hub if you want Cloudflare to run the HTTPS service and store team data in a SQLite Durable Object on Workers Paid. Developer setup is the same on both. [Compare the hubs](hub.md#choose-a-hub) · [Deploy to Cloudflare](cloudflare.md)

## Get the binaries and service files

Use a Linux or macOS host with a local filesystem that supports file locks and atomic renames. Install jq and curl 7.55.0 or later. The service installation steps require `sudo`. For remote access, you also need a DNS name and an HTTPS reverse proxy on the hub host.

Install the release binaries and keep them on your `PATH` for this guide. Then download the example policy and service files from the same tag. [Install OpenPlexer](install.md)

```sh
umask 077
hub_root="$HOME/openplexer-hub"
mkdir -p "$hub_root"
hub=$(command -v openplexer-hub)
tag=$("$hub" --version)
source_url="https://raw.githubusercontent.com/Multiplexer-si/OpenPlexer/refs/tags/$tag"
curl -fsSL "$source_url/spec/policy/v1/examples/bundle/minimal.json" -o "$hub_root/minimal.json"
curl -fsSL "$source_url/deploy/openplexer-hub.service" -o "$hub_root/openplexer-hub.service"
curl -fsSL "$source_url/deploy/com.openplexer.hub.plist" -o "$hub_root/com.openplexer.hub.plist"
```

To use a source checkout, start with [Build from source](#build-from-source).

## Initialize the data directory and publish a policy

Start with a new data directory. `init` creates `state.json` and generates the signing key. The setup saves member tokens in a fresh private directory.

```sh
data_dir="$hub_root/data"
project=prj-shop
repo=api

mkdir -m 700 "$data_dir" &&
"$hub" init -data-dir "$data_dir" &&
tokens=$(umask 077 && mktemp -d "$hub_root/tokens.XXXXXX") &&
(
  umask 077 &&
  "$hub" add-member -data-dir "$data_dir" -id mem-lee -role lead > "$tokens/mem-lee.token" &&
  "$hub" add-member -data-dir "$data_dir" -id mem-ana > "$tokens/mem-ana.token" &&
  "$hub" add-member -data-dir "$data_dir" -id mem-sam > "$tokens/mem-sam.token"
) &&
"$hub" add-project -data-dir "$data_dir" -id "$project" -repo "$repo" &&
jq --arg project "$project" --arg repo "$repo" \
	'.project_id = $project | .repos = [$repo]' \
	"$hub_root/minimal.json" > "$hub_root/bundle.json" &&
"$hub" publish-policy -data-dir "$data_dir" -project "$project" -file "$hub_root/bundle.json"
```

Each `add-member` prints its bearer token once. Give each member their own token through a private channel. The `lead` role allows Lee to publish a policy over HTTP. The example bundle has no member grants. To use it for team policy enforcement, define roles and grants for your member IDs and raise `version` before publishing the edit. [Policy format](../spec/policy/README.md)

## Verify the hub on loopback

Start the hub in the background and keep this shell open for the remaining commands.

```sh
listen=127.0.0.1:8787
hub_url="http://$listen"
"$hub" serve -data-dir "$data_dir" -listen "$listen" > "$hub_root/hub.log" 2>&1 &
hub_pid=$!
```

Fetch the public key set and the signed bundle for each member. The local CLI should report `policy: verified` for each bundle. `OPENPLEXER_HOME` puts each check in an isolated directory.

```sh
curl --noproxy 127.0.0.1 --fail --silent --show-error --max-time 2 \
	--retry 5 --retry-connrefused --retry-delay 1 \
	"$hub_url/.well-known/jwks.json" -o "$hub_root/keys.json"
for member in mem-lee mem-ana mem-sam; do
	printf 'Authorization: Bearer %s\n' "$(cat "$tokens/$member.token")" |
		curl --noproxy 127.0.0.1 --fail --silent --show-error --max-time 2 --header @- \
			"$hub_url/api/v1/projects/$project/policy.bundle" -o "$hub_root/$member.bundle.json"
	OPENPLEXER_HOME="$hub_root/check-$member" openplexer \
		configure --keys "$hub_root/keys.json" --member "$member"
	OPENPLEXER_HOME="$hub_root/check-$member" openplexer \
		policy load "$hub_root/$member.bundle.json"
done
```

If curl cannot connect, check `hub.log` in `hub_root`. Stop the test hub before starting the service.

```sh
kill "$hub_pid"
wait "$hub_pid" 2>/dev/null || :
```

From the checkout, `deploy/smoke.sh` runs these steps in a temporary directory and exits `0` when all three bundles verify. If the OS denies the loopback bind, the script explains the failure and exits `77`.

## Install the service

Choose the steps for your host. On the first installation, copy the initialized data directory to `/var/lib/openplexer`. If that path already holds hub state, use the upgrade steps instead. Once the service starts, use `/var/lib/openplexer` for admin commands and backups. The copy in `hub_root` is a snapshot from before the service started.

### Linux with systemd

Run the service under its own account. Use `sudo -u openplexer` to run admin commands that access the service data directory.

```sh
sudo useradd --system --user-group --home-dir /var/lib/openplexer --shell /usr/sbin/nologin openplexer
sudo install -d -m 0700 -o openplexer -g openplexer /var/lib/openplexer
sudo cp -pR "$data_dir/." /var/lib/openplexer/
sudo chown -R openplexer:openplexer /var/lib/openplexer
sudo mkdir -p /usr/local/bin
sudo install -m 0755 "$hub" /usr/local/bin/openplexer-hub
sudo install -m 0644 "$hub_root/openplexer-hub.service" /etc/systemd/system/openplexer-hub.service
sudo systemctl daemon-reload
sudo systemctl enable --now openplexer-hub
sudo systemctl status openplexer-hub
```

Read service logs with `sudo journalctl -u openplexer-hub`. [systemd unit](../deploy/openplexer-hub.service)

### macOS with launchd

Use a LaunchDaemon so the hub starts at boot. Run these commands from the hub operator's regular account. The plist sets the service to run as that account.

```sh
sudo install -d -m 0700 -o "$(id -un)" -g "$(id -gn)" /var/lib/openplexer
sudo cp -pR "$data_dir/." /var/lib/openplexer/
sudo mkdir -p /usr/local/bin
sudo install -m 0755 "$hub" /usr/local/bin/openplexer-hub
plist=/Library/LaunchDaemons/com.openplexer.hub.plist
sudo install -o root -g wheel -m 0644 "$hub_root/com.openplexer.hub.plist" "$plist"
sudo plutil -replace UserName -string "$(id -un)" "$plist"
plutil -lint "$plist"
sudo launchctl bootstrap system "$plist"
sudo launchctl print system/com.openplexer.hub
```

Read service errors in `/var/lib/openplexer/hub.log`. [LaunchDaemon plist](../deploy/com.openplexer.hub.plist)

## Give teammates an HTTPS URL

Use HTTPS for any connection off the hub host. The agent's hub transport requires TLS for non-loopback URLs. Keep the hub listener on `127.0.0.1:8787` and expose the reverse proxy on the network.

For a public DNS name, point its records at the host and allow inbound TCP ports `80` and `443` to Caddy. Install Caddy on the same host and set its Caddyfile to the following, replacing `hub.example.com` with your DNS name. Caddy obtains and renews the TLS certificate. [Install Caddy](https://caddyserver.com/docs/install) · [Automatic HTTPS](https://caddyserver.com/docs/automatic-https)

```caddyfile
hub.example.com {
	reverse_proxy 127.0.0.1:8787
}
```

Load the Caddyfile into Caddy's service. Check `https://hub.example.com/.well-known/jwks.json` from a teammate's machine to confirm the certificate and proxy work. Give each teammate the HTTPS URL, project ID, their member ID and token, and the trusted policy key set from `$hub_root/keys.json`. [Run Caddy as a service](https://caddyserver.com/docs/running)

To load the policy by hand, install OpenPlexer and save your token as `member.token` in a private directory. Run these commands from that directory with your team's URL, project ID, and your member ID. [Install OpenPlexer](install.md)

```sh
hub_url=https://hub.example.com
project=prj-shop
member=mem-ana
curl --fail --silent --show-error "$hub_url/.well-known/jwks.json" -o keys.json
printf 'Authorization: Bearer %s\n' "$(cat member.token)" |
	curl --fail --silent --show-error --header @- \
		"$hub_url/api/v1/projects/$project/policy.bundle" -o signed-bundle.json
openplexer configure --keys keys.json --member "$member"
openplexer policy load signed-bundle.json
```

Repeat the manual load when the policy changes. For automatic policy refresh and live Room delivery, configure the connection with `openplexer configure --hub`. Then run `openplexer policy sync` and `openplexer daemon`. [Connect to a team hub](local-policy.md#connect-to-a-team-hub)

## Connect each developer's agent

1. Configure the team hub connection and sync the first policy. [Connect to a team hub](local-policy.md#connect-to-a-team-hub)
2. Install and trust the hook for your agent. Start the agent with the same `OPENPLEXER_HOME` you used to sync the policy. [Claude Code setup](hooks-claude-code.md#set-up) or [Codex setup](hooks-codex.md#set-up)
3. Run `openplexer daemon` in another terminal with the same `OPENPLEXER_HOME`. Keep it running to refresh policy and receive Room events. [Room daemon and posts](local-policy.md#connect-to-a-team-hub)

On the hub host, export the team's audit log so a reviewer can verify it offline. [Export and verify an audit log](audit.md)

## Back up and upgrade

Stop the hub and pause admin commands before taking a backup. On Linux, use `sudo systemctl stop openplexer-hub`. On macOS, use `sudo launchctl bootout system /Library/LaunchDaemons/com.openplexer.hub.plist`.

Save each archive in a fresh private directory.

```sh
backup_dir=$(umask 077 && mktemp -d "$hub_root/backup.XXXXXX") &&
backup="$backup_dir/openplexer-$(date +%Y%m%d-%H%M%S).tar" &&
(umask 077 && sudo tar -C /var/lib -cf - openplexer > "$backup")
```

Store the archive securely off the hub host. It contains the signing private key, token hashes, the current signed policies, Room history, and active claims. To restore on the same host, stop the service, move the existing data directory aside, and extract with `sudo tar -C /var/lib -xpf "$backup"`. Check that `/var/lib/openplexer` is private and owned by the service account before restarting. Restoring an older backup also rolls back policy versions, membership, and Room sequence numbers.

For an upgrade, install the intended version from its release archive and run `hub=$(command -v openplexer-hub)`. Stop and back up the service, then replace the installed binary with `sudo install -m 0755 "$hub" /usr/local/bin/openplexer-hub`. Reuse the data directory so the signing key survives the upgrade. To resume service after a backup, restore, or upgrade, run `sudo systemctl start openplexer-hub` on Linux or `sudo launchctl bootstrap system /Library/LaunchDaemons/com.openplexer.hub.plist` on macOS. Fetch the keys and bundle again to check the running hub. [Install OpenPlexer](install.md)

## Operate within the hub's limits

Run one hub process on one host per data directory. All active members can access every project and Room in the single team. Each operation reads the whole state snapshot into memory. The hub keeps Room history indefinitely, so disk use, memory use, and write time grow as events accumulate. Keep secrets and code out of Room event bodies. The hub validates the event schema and leaves content checks to the team. The signing key is created at initialization and key rotation is pending. [Data directory and operational limits](hub.md#data-directory-and-operational-limits)

## Build from source

Install Git and Go 1.27 or later. Clone the repository and record the commit so you can rebuild the same version.

```sh
git clone https://github.com/Multiplexer-si/OpenPlexer.git
cd OpenPlexer
git rev-parse HEAD

umask 077
hub_root="$HOME/openplexer-hub"
mkdir -p "$hub_root/bin"
go build -o "$hub_root/bin/" ./apps/...
export PATH="$hub_root/bin:$PATH"
hub="$hub_root/bin/openplexer-hub"
cp spec/policy/v1/examples/bundle/minimal.json deploy/openplexer-hub.service deploy/com.openplexer.hub.plist "$hub_root/"
```

Continue with [Initialize the data directory and publish a policy](#initialize-the-data-directory-and-publish-a-policy).
