# xo-talos

Builds a Xen Orchestra VM template from the latest stable Talos release, ready
for [cluster-api-provider-vates](https://github.com/vatesfr/cluster-api-provider-vates)
(CAPV).

The template meets the requirements of the CAPV Talos guide:

- `nocloud` image from the [Talos Image Factory](https://factory.talos.dev)
  with the `siderolabs/xen-guest-agent` extension;
- created from the RAW image and never booted;
- `viridian: false` (otherwise XO prepends an MBR to the cloud-init config
  drive and Talos stays in maintenance mode).

## What it does

1. Registers a schematic with the requested extensions and resolves the
   latest stable Talos version (pre-releases are ignored).
2. Downloads `nocloud-<arch>.raw.zst` (or `.raw.xz`). The raw disk is never
   written: it is decompressed on the fly and converted to a dynamic VHD that
   skips empty 2 MiB blocks.
3. In Xen Orchestra: creates a VDI, uploads the raw image into it, creates a
   VM from "Other install media", attaches the disk, sets `viridian=false`,
   the firmware, CPUs and tags, then converts the VM to a template.
4. Prints the template UUID, the value for CAPV's `XO_TEMPLATE_UUID`.

If a template with the same name already exists, it prints its UUID and does
nothing else. If a step fails, the VM and VDI it created are deleted.

The template is named `talos-<version>-nocloud-<arch>-<schematic prefix>` and
tagged `talos`, `talos-version=<version>`, `talos-platform=nocloud`,
`talos-arch=<arch>` and `talos-schematic=<prefix>`. Use the same version for CAPV's `TALOS_VERSION`.

CAPV's Talos guide uses a different kubelet patch format for Talos 1.13 and
1.14, so pin the minor line your cluster templates expect, e.g.
`--talos-version v1.13` to follow 1.13 patch releases.

## Usage

```sh
go build ./cmd/xo-talos

export XOA_ENDPOINT=https://xo.example.com
export XOA_TOKEN=...          # or XOA_USERNAME / XOA_PASSWORD
# export XOA_INSECURE=true    # self-signed certificate

XO_TEMPLATE_UUID=$(./xo-talos --sr <sr-uuid>)
```

Progress goes to stderr; stdout carries only the result, in the `--output`
format:

- `text` (default): the template UUID;
- `json`: an array with, per pool, `template_uuid`, `template_name`,
  `pool_uuid`, `sr_uuid`, `talos_version`, `schematic_id`, `created` and
  `verified`;
- `env`: shell exports of the clusterctl variables used by CAPV's Talos
  templates:

  ```sh
  eval "$(./xo-talos --sr <sr-uuid> --output env)"
  # XO_TEMPLATE_UUID, XO_POOL_UUID and TALOS_VERSION are now set
  clusterctl generate cluster my-cluster --infrastructure vates --flavor talos ...
  ```

| Flag | Default | Description |
|---|---|---|
| `--sr` | `$XO_SR_UUID` | SR that stores the disk (required); repeat it, one SR per pool, to build in several pools |
| `--network` | none | add a VIF on this network, at most one per pool (`$XO_NETWORK_UUID`). CAPV adds its own VIFs, so leave it empty for CAPV |
| `--talos-version` | `latest` | `latest` (newest stable), a minor line such as `v1.13` (its newest patch), or an exact version such as `v1.14.5` |
| `--schematic` | none | Image Factory schematic YAML file, see below |
| `--extensions` | none | comma-separated official extensions to add |
| `--arch` | `amd64` | image architecture |
| `--firmware` | `bios` | `bios` or `uefi` |
| `--cpus` | `2` | vCPUs |
| `--memory` | `2048` | memory in MiB |
| `--name` | generated | template name |
| `--base-template` | `Other install media` | diskless template the VM is created from |
| `--work-dir` | `$TMPDIR` | where the compressed image is downloaded (a few hundred MB) |
| `--disk-format` | `vhd` | `vhd` uploads only the blocks holding data (about 240 MiB instead of 4.1 GiB for Talos v1.14.5); `raw` uploads the whole disk |
| `--keep-image` | `false` | keep the downloaded image; later runs reuse it |
| `--timeout` | `30m` (`$XOA_TIMEOUT`) | timeout of each XO request, including the upload |
| `--factory-url` | `https://factory.talos.dev` | Image Factory URL |
| `--output` | `text` | `text`, `json` or `env`, see above |
| `--prune-keep` | `0` | delete older templates of the same flavor, keeping this many Talos versions, see below |
| `--prune-dry-run` | `false` | with `--prune-keep`, only list what would be deleted |
| `--debug` | `false` | also print the Xen Orchestra SDK logs |
| `--verify` | `false` | boot-test the template, see below |
| `--verify-network` | `$XO_VERIFY_NETWORK_UUID` | network of the test VM, one per pool (required with `--verify`) |
| `--verify-timeout` | `10m` | how long the boot test may take |

## Several pools

A template belongs to one pool. Give one `--sr` per pool (or a
comma-separated `XO_SR_UUID`) to build them all in one run: the image is
downloaded and scanned once, then uploaded to each pool. Networks given with
`--network` and `--verify-network` are matched to their pool automatically.

```sh
./xo-talos --sr <sr-in-pool-a> --sr <sr-in-pool-b> \
  --verify --verify-network <net-in-pool-a> --verify-network <net-in-pool-b> \
  --output json
```

If one pool fails, the others still go on; the output lists the templates
that succeeded and the exit code is 1.

## Verifying the template

`--verify` proves the template boots, without ever starting the template
itself:

1. clones it into a test VM `xo-talos-verify-<id>` with one VIF on
   `--verify-network`, and boots the clone;
2. waits for xen-guest-agent to report the clone's IP address;
3. waits for the Talos API to accept a TLS connection on port 50000
   (maintenance mode), so the machine running the tool must reach that
   network;
4. deletes the test VM and its disk, whatever the outcome.

If a template built by this run fails verification, it is deleted, so CAPV
never picks it up and the next run rebuilds it. An existing template that
fails is only reported. `--verify` also works on an existing template.

## Customizing the image

`--extensions` adds official system extensions. `--schematic` takes a full
[Image Factory schematic](https://github.com/siderolabs/image-factory#schematics),
so you can also set `extraKernelArgs`, `meta`, `secureboot`, and so on:

```yaml
# schematic.yaml
customization:
  extraKernelArgs:
    - console=ttyS0
  systemExtensions:
    officialExtensions:
      - siderolabs/iscsi-tools
```

```sh
./xo-talos --sr <sr-uuid> --schematic schematic.yaml --extensions siderolabs/util-linux-tools
```

`siderolabs/xen-guest-agent` is always added. Every extension is checked
against the Talos version, and the factory rejects unknown fields. Each
schematic has its own ID, which is part of the template name, so variants
live side by side. Avoid `embeddedMachineConfiguration`: CAPV provides the
machine configuration through cloud-init (the tool prints a warning).

## Automation

- **CI** (`.github/workflows/ci.yml`): formatting, `go vet`, golangci-lint,
  tests (also with the race detector) and build, on every push and pull
  request.
- **Releases** (`.github/workflows/release.yml`): pushing a `vX.Y.Z` tag runs
  CI, then GoReleaser builds Linux, macOS and Windows binaries into a draft
  GitHub release.
- **Weekly template build** (`.github/workflows/build-template.yml`): every
  Monday, and on demand, builds the template of the latest stable Talos
  release (a no-op when it exists) and lists the templates in the job
  summary. Set the `XOA_ENDPOINT` and `XOA_TOKEN` secrets and the
  `XO_SR_UUID` variable to enable it; the workflow file lists the optional
  variables (`XO_VERIFY_NETWORK_UUID`, `TALOS_VERSION`, `TEMPLATE_RUNNER` for a
  self-hosted runner that can reach Xen Orchestra, ...).

## Deleting old templates

By default nothing is deleted. `--prune-keep N` deletes, after the build,
the older templates of the same flavor in each pool and keeps the `N` newest
Talos versions:

- only templates this tool built are considered: the name must be
  `talos-<version>-<platform>-<arch>-<schematic prefix>` and the template
  must carry the matching `talos-schematic=` tag;
- only the same schematic, platform and arch: a template with other
  extensions or kernel arguments is never touched;
- the template of the current run is always kept, even when it is older.

Try it with `--prune-dry-run` first. VMs cloned from a deleted template keep
working (checked on XCP-ng: a clone still boots after its template is
deleted), but CAPV can no longer create machines from it: move your `XOMachineTemplate`s to the new
template before pruning the old one.
