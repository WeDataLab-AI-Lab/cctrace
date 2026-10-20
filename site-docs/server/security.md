# Security operations

How to let clients update themselves from your server, how to sign a server policy, and how to keep the keys behind both.

A server built as in [Install](install.md), with `UPDATE_SIGNING=optional` and no keys, uses none of this. Its clients link empty public keys: they refuse automatic updates and never apply a server policy. Everything on this page starts from key pairs you generate yourself.

## Two trust roots

| Key pair | Signs | Clients accept it for | Its private key is used by |
|---|---|---|---|
| Update key | The manifest beside each client binary in `/downloads` | Updates and server policies | The image build, as a BuildKit secret |
| Policy key | Server policies | Server policies only | The policy signing tool, each time you sign a policy |

A client trusts only the public keys linked into it at build time, never a key a server hands it. It checks an update manifest against its update public keys. It checks a policy against its policy public keys and its update public keys together. Each of the two build values takes a comma-separated list, and a signature made with any listed key passes.

Use two different key pairs. A policy rewrites every server address a client uses and the certificate authority it trusts; an update replaces the binary itself. With separate pairs, the key you take out to sign policies cannot sign a binary that clients install. The image build refuses a policy public key that repeats an update public key. The update key stays trusted for policies so that a policy can still be signed if the policy private key is lost.

## Generate a key pair

Both pairs are raw Ed25519 keys in standard base64:

- Public key: 32 bytes, one line of 44 characters.
- Private key: 64 bytes in a file, the 32-byte seed followed by the 32-byte public key.

OpenSSL writes keys in DER or PEM containers, which neither the build nor the signing tools accept. The commands below take the raw bytes out of DER files. They need OpenSSL 1.1.1 or later. The LibreSSL that macOS ships answers `Algorithm ed25519 not found`.

Keep the keys in a directory outside the repository, so that no build context can pick them up. The examples on this page set `KEYS=../cctrace-keys`, a directory next to your clone, and run from the repository root:

```console
$ KEYS=../cctrace-keys
$ mkdir -p "$KEYS" && chmod 700 "$KEYS"
$ umask 077
$ openssl genpkey -algorithm ed25519 -outform DER -out "$KEYS/update.der"
$ openssl pkey -inform DER -in "$KEYS/update.der" -pubout -outform DER -out "$KEYS/update.pub.der"
$ tail -c 32 "$KEYS/update.pub.der" | openssl base64 -A > "$KEYS/update-public-key.txt"
$ cat <(tail -c 32 "$KEYS/update.der") <(tail -c 32 "$KEYS/update.pub.der") \
    | openssl base64 -A > "$KEYS/update-private-key"
$ rm "$KEYS/update.der" "$KEYS/update.pub.der"
```

The last 32 bytes of the DER private key are the seed, and the last 32 bytes of the DER public key are the public key. Run the same commands with `policy` in place of `update` to make the policy pair.

Check the format before you use a key:

```console
$ openssl base64 -d -A < "$KEYS/update-public-key.txt" | wc -c
32
$ openssl base64 -d -A < "$KEYS/update-private-key" | wc -c
64
$ openssl base64 -d -A < "$KEYS/update-private-key" | tail -c 32 | openssl base64 -A \
    | cmp - "$KEYS/update-public-key.txt" && echo "pair ok"
pair ok
```

The third command confirms that the private key file ends with the public key you build into clients. A bare 32-byte seed or a PEM file does not decode to 64 bytes, and the signing tools refuse it.

## Build the keys into clients

A client links the keys through the Go linker flags `-X main.updatePublicKeyBase64=...` and `-X main.policyPublicKeyBase64=...`, which the builds below set for you. Two kinds of build produce clients:

| Build | Where the keys go |
|---|---|
| The server image, for the binaries it serves at `/downloads` | `--build-arg UPDATE_PUBLIC_KEY=...` and `--build-arg POLICY_PUBLIC_KEY=...`; see [the next section](#build-a-signed-server-image) |
| `make build-client`, `build-linux`, `build-windows`, `build`, `install` | The Make variables `UPDATE_PUBLIC_KEY` and `POLICY_PUBLIC_KEY` |

For make, pass the variables on the command line, or set them in the local-defaults.env file that also holds the default endpoints ([Install the client](../client/install.md)):

```console
$ make build-client UPDATE_PUBLIC_KEY="$(cat "$KEYS/update-public-key.txt")" \
    POLICY_PUBLIC_KEY="$(cat "$KEYS/policy-public-key.txt")"
```

`make print-update-public-key` and `make print-policy-public-key` print the values make would link. They print empty lines when neither the command line nor that file sets them, and a client built then links empty keys. The image build does not read local-defaults.env.

## Build a signed server image

The image build signs a manifest for each client binary it serves. With `UPDATE_SIGNING=required`, the default, it needs three inputs and stops if any is missing: both public keys as build arguments, and the update private key as the BuildKit secret `update_signing_key`.

```console
$ docker build -f deploy/Dockerfile \
    --build-arg VERSION=v1.0.0 \
    --build-arg UPDATE_PUBLIC_KEY="$(cat "$KEYS/update-public-key.txt")" \
    --build-arg POLICY_PUBLIC_KEY="$(cat "$KEYS/policy-public-key.txt")" \
    --secret id=update_signing_key,src="$KEYS/update-private-key" \
    -t cctrace/cctraced:latest .
```

- `VERSION` is the version the server reports and the version stamped into every client binary in the image. Clients update only to a higher version of the form v1.2.3 ([below](#when-a-client-updates-itself)). Without it both are `dev`, and no client updates.
- `--secret` needs BuildKit, the builder `docker build` uses by default since Docker Engine 23.0. The secret is mounted for the signing step only. It does not enter the build context, a layer, or the image. Pass the private key only this way: not as a build argument, and not as a file under the repository, which is the build context the build copies.
- Without the secret, the signing step fails with exit code 1 and no message of its own.
- The signing step checks that the private key belongs to one of the keys in `UPDATE_PUBLIC_KEY`, and otherwise stops with `signing private key is not among the declared update public keys`.
- `POLICY_PUBLIC_KEY` is required in this mode too. Each entry must be a 32-byte key in standard base64, and no entry may repeat one in `UPDATE_PUBLIC_KEY`.
- Endpoint defaults for the served binaries are passed the same way, as `DEFAULT_SYNC_ENDPOINT` and `DEFAULT_OTEL_ENDPOINT`.

Check the result. The image should hold five binaries, a manifest beside each, and no secret:

```console
$ docker run --rm --entrypoint sh cctrace/cctraced:latest -c \
    'ls -1 /downloads; test ! -e /run/secrets/update_signing_key && echo "no secret"'
cctrace-darwin-amd64
cctrace-darwin-amd64.manifest.json
cctrace-darwin-arm64
cctrace-darwin-arm64.manifest.json
cctrace-linux-amd64
cctrace-linux-amd64.manifest.json
cctrace-linux-arm64
cctrace-linux-arm64.manifest.json
cctrace-windows-amd64.exe
cctrace-windows-amd64.exe.manifest.json
no secret
```

A manifest names its file and carries the file's SHA-256, the version, and two signatures: one over the file name and checksum, and one that adds the version.

`/downloads` is a directory inside the `cctraced` image. The compose file mounts no volume over it, so the binaries and manifests change only when you rebuild the image and recreate the container, as in [Upgrade](operations.md#upgrade). Each release is one signed build with a higher `VERSION`.

## When a client updates itself

A client replaces its own binary only when all of these hold:

- It links an update public key: it was downloaded from a signed image, or built by make with `UPDATE_PUBLIC_KEY`.
- Its own version has the form v1.2.3, and the server reports a higher one. A suffix after the third number, as in v1.2.3-rc1, is ignored when comparing. A `dev` build never updates. Neither does a make build in a clone without tags, whose version is a bare commit hash; pass `VERSION=v1.2.3` to make.
- Its sync endpoint uses HTTPS, or plain HTTP to `localhost` or to an IP address in a loopback, private (RFC 1918, RFC 4193) or link-local range. Plain HTTP to a host name is refused, because the client cannot tell where the name resolves. A redirect to an address outside these rules is refused as well.
- The manifest verifies with one of its update public keys, both signatures pass, the manifest's version equals the version the server reports, and the downloaded file matches the manifest's checksum.
- The user running the client can write to the directory that holds the binary. The update writes its new file next to the old one, so a binary in a directory only root can write to, such as a root-owned `/usr/local/bin`, cannot update itself.

A one-shot `cctrace sync` checks before it uploads. The sync daemon checks when it starts and about every five minutes while it runs. A failed attempt leaves an `update:` line in the profile's log (`~/.cctrace/sync.log` for the default profile), and collection continues on the current binary; after a failure the daemon waits 15 minutes, then 30, then an hour between attempts at the same version, while a `cctrace sync` you run by hand tries at once. **Admin > Clients** in the dashboard shows which version each client last reported.

For a private network on routable addresses, which the client cannot recognize, setting `CCTRACE_UPDATE_ALLOW_INSECURE_ENDPOINT=1` in the client's environment skips the transport rule. The signature and checksum checks still apply.

## Store the private keys

- Never keep a private key on the server that serves `/downloads`. Signing exists so that clients refuse a forged binary even when that server is taken over; a key on the same host hands an attacker the binary and the signature together.
- Keep each private key file at mode 0600, outside the repository and every build context. The image build does not use the policy private key; take it out only to sign a policy.
- Keep an offline copy of each private key, for example on removable media in a locked place, and keep the update and policy copies apart. After making a copy, compare it with the original and check it against the public key:

```console
$ cmp "$KEYS/update-private-key" "<copy>"
$ openssl base64 -d -A < "<copy>" | tail -c 32 | openssl base64 -A \
    | cmp - "$KEYS/update-public-key.txt" && echo "pair ok"
```

## Rotate the update key

A client accepts a manifest that any of its update public keys verifies. Rotate through a period in which clients trust both keys:

1. Generate the new pair.
2. Release with `UPDATE_PUBLIC_KEY="<old>,<new>"`, still signing with the old private key.
3. Wait until every client runs that release (**Admin > Clients**).
4. Release signed with the new private key. Keep both keys in the list.
5. In a later release, list only the new key.

Each step is a release, so `VERSION` goes up each time. A client that has not updated to the release of step 2 by the time of step 4 trusts only the old key. It refuses each new release with `update manifest signature verification failed`, keeps running the binary it has, and needs one manual reinstall.

## Rotate the policy key

The same overlap, through `POLICY_PUBLIC_KEY`:

1. Generate the new policy pair.
2. Release with `POLICY_PUBLIC_KEY="<old>,<new>"`.
3. Once every client runs that release, sign new policies with the new private key.
4. In a later release, list only the new key.

A client that has not reached the release in step 2 refuses a policy signed with the new key: `untrusted policy signature`.

## If a key is lost

- **Update private key, with no copy.** Nothing clients trust can sign a new release. Generate a new pair, release with the new key only, and reinstall every client by hand once, downloading the binary from `/downloads` as in [Install the client](../client/install.md). Automatic updates work again from there.
- **Policy private key.** Clients also trust the update key for policies, so sign the next policy with the update private key. Release a new policy public key in place of the lost one; once clients run it, sign policies with the new policy key.
- **A private key that may have been copied** counts as lost. Clients keep accepting whatever it signs until they run a release that no longer lists its public key.

## Sign a server policy

A policy moves profiles to new addresses on the same server host: from plaintext to HTTPS, to other ports, and to a new certificate authority. It cannot move a client to another host. The server hands the signed file to the clients the policy names, and each client checks the signature, the validity window, and the policy number itself. How the server serves the file is in [Operations](operations.md#server-policy); what a client changes when it applies a policy is in [Sync daemon](../client/sync.md#server-policy).

### Fields

| Field | Value |
|---|---|
| `schema` | `1` |
| `sequence` | The policy number, greater than 0. See [Policy numbers and validity](#policy-numbers-and-validity). |
| `server_host` | The host of every address in the policy: lowercase, without scheme or port |
| `not_before`, `expires_at` | The validity window, in RFC 3339, for example `2026-11-02T00:00:00Z` |
| `targets` | An array of `{"installation_id": ..., "profile": ...}`. `profile` is empty for the default profile. |
| `all_targets` | `true` names every installation and profile that asks this server; `targets` must then be empty. Omit it otherwise. |
| `from` | An array of the address sets the policy moves from, each `{"sync": ..., "otel": ..., "protocol": ...}` |
| `to` | The address set the policy moves to |
| `ca_pem` | One or more CA certificates in PEM, as one string |
| `ca_sha256` | The SHA-256 of the `ca_pem` bytes, in lowercase hex |

The signing tool and every client check:

- Every address uses `server_host` and an explicit port. The path is empty or `/`. User info, a query, or a fragment is refused.
- Each `protocol` is grpc or http/protobuf. In `to`, both addresses start with `https://` and the protocol is http/protobuf.
- Every certificate in `ca_pem` is a CA certificate.
- No unknown field, and no more than 256 KiB once signed.

Where the values come from:

- `installation_id` and `profile`: `cctrace config policy status` on the client prints both. A client that links a key also reports them, with its current addresses, after each policy check of a logged-in profile. The reports are kept in the `client_policy_reports` table and have no dashboard page or API ([Privacy](../reference/privacy.md)).
- `from`: a set matches a profile only when its three values equal, as strings, what `cctrace config get server.sync_endpoint`, `cctrace config get server.endpoint` and `cctrace config get server.protocol` print on the client.
- `to`: before it changes anything, the client asks `/api/health` and `/api/version` at the new `sync` address, and posts empty requests to `/v1/logs` and `/v1/metrics` at the new `otel` address with the profile's upload token, trusting the system roots and `ca_pem`. Any answer other than 200 stops the policy. So the new `otel` address is an OTLP/HTTP one, and the TLS listeners must be up before you publish. With the [HTTPS overlay](install.md#optional-https-with-caddy), the new `sync` address is on port 8443 and the new `otel` address on 5318.
- `ca_pem`: the CA that issued the server's certificate, for example the CA file exported in [Certificates](install.md#certificates).

### Write and sign

jq builds the policy file and escapes the line breaks of the PEM. This example moves the default profile of one installation from the plaintext ports to the HTTPS overlay, on a server named cctrace.example.com whose internal CA was exported as cctrace-ca.crt:

```console
$ jq -n --rawfile ca cctrace-ca.crt \
    --arg sum "$(openssl dgst -sha256 -r cctrace-ca.crt | cut -d' ' -f1)" '{
  schema: 1,
  sequence: 1,
  server_host: "cctrace.example.com",
  not_before: "2026-11-02T00:00:00Z",
  expires_at: "2026-11-16T00:00:00Z",
  targets: [{installation_id: "<installation ID>", profile: ""}],
  from: [{sync: "http://cctrace.example.com:8080", otel: "http://cctrace.example.com:4317", protocol: "grpc"}],
  to: {sync: "https://cctrace.example.com:8443", otel: "https://cctrace.example.com:5318", protocol: "http/protobuf"},
  ca_pem: $ca,
  ca_sha256: $sum
}' > policy.json
```

Sign it with the policy private key. `-public-key` is the list your clients trust for policies:

```console
$ go run ./scripts/sign-server-policy.go \
    -private-key "$KEYS/policy-private-key" \
    -public-key "$(cat "$KEYS/policy-public-key.txt"),$(cat "$KEYS/update-public-key.txt")" \
    -out server-policy.json \
    policy.json
```

- The tool runs the same field checks a client runs and stops at the first problem, for example `sign policy: destination requires HTTPS and http/protobuf`.
- It checks that the signing key is in `-public-key`, not that your clients were built with that list.
- The signed file holds the policy and its signature, and no secret. The tool writes it with mode 0600; make it readable by the server, for example with `chmod 644`, before you put it in place.

Copy the signed file to the server and put it in place in one step, as described in [Operations](operations.md#server-policy): copy it into the mounted directory under a temporary name, then rename it over the old one.

### Policy numbers and validity

A client keeps, per profile, the number of the last policy it applied, and compares each policy it receives with it:

| Received number | Result |
|---|---|
| Lower | Refused: `policy sequence <n> is older than applied sequence <m>` |
| Equal, with the same content, and the profile still on `to` | Already applied. Nothing happens. |
| Equal, with different content, or the profile no longer on `to` | Refused: `applied policy changed; require a higher policy sequence or explicit manual recovery` |
| Higher | Checked against `targets` and `from`, then applied |

- The server does not check dates. A client refuses a policy before `not_before`, and from `expires_at` on, with `policy outside validity interval`. It records that as a failed check and repeats it at every check, about every five minutes for a running daemon, as long as the file stays in place.
- To extend a policy or change anything in it, sign it again with a higher number. New dates under the same number are new content, which clients that applied the policy refuse.
- A higher number also reaches profiles that applied an earlier policy, when `targets` names them or `all_targets` is set. Their current addresses are the earlier policy's `to`, so list that set in `from` next to the plaintext one, or they fail with `current endpoints are not authorized by policy`. They then apply the new policy: their addresses stay, and they write the new `ca_pem` to a new CA file.
- With `all_targets`, every profile that asks this server receives the policy, and each one whose current set is not in `from` fails with `current endpoints are not authorized by policy` at every check.
- To stop handing out a policy, clear `CCTRACE_SERVER_POLICY_FILE` and recreate the container; the server then answers 204. Deleting the file instead makes it answer 503. Stopping does not move anyone back: a profile that applied a policy keeps its new addresses.

## TLS

A policy can only move clients to HTTPS that is already running. Set it up first: [Optional: HTTPS with Caddy](install.md#optional-https-with-caddy) and [Certificates](install.md#certificates).

- The internal CA lives in the `caddy_data` volume. Removing the volume creates a new CA, and every client that trusts only the old one stops connecting. That includes clients a policy moved, which trust the `ca_pem` of that policy.
- The overlay's first step binds the plaintext ports to loopback. Leave those bind settings out and the plaintext ports stay reachable beside the TLS ones. A move by policy needs that: a client on the plaintext addresses fetches the policy from them, and keeps sending there until it applies it. The cost is that everything on the plaintext ports crosses the network unencrypted: upload tokens, dashboard passwords and session tokens, and the conversation content that sync uploads. Bind them to loopback once every client has moved.
- A client that a policy moved remembers the plaintext sync address from that policy's `from`. When its HTTPS address cannot be reached, or its certificate is not trusted, it asks the plaintext address for a policy, without credentials. Closing the plaintext ports removes that way back for a client that missed a CA change.
