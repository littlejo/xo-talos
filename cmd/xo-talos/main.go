// Command xo-talos downloads a Talos nocloud image from the Talos
// Image Factory and turns it into Xen Orchestra VM templates for
// cluster-api-provider-vates, one per pool.
//
// Progress goes to stderr; stdout only carries the result (--output), so it
// can be captured: XO_TEMPLATE_UUID=$(xo-talos ...).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/config"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
	xov2 "github.com/vatesfr/xenorchestra-go-sdk/v2"
	"github.com/vatesfr/xenorchestra-go-sdk/v2/client"

	"github.com/littlejo/xo-talos/internal/factory"
	"github.com/littlejo/xo-talos/internal/image"
	"github.com/littlejo/xo-talos/internal/progress"
	"github.com/littlejo/xo-talos/internal/xo"
)

// version is set at build time by GoReleaser.
var version = "dev"

const platform = "nocloud"

// requiredExtension is always added to the schematic: CAPV needs the guest
// agent to read the VM's IP addresses.
const requiredExtension = "siderolabs/xen-guest-agent"

type options struct {
	srs, networks, verifyNetworks uuidList
	talosVersion                  string
	schematicFile                 string
	extensions                    string
	arch                          string
	firmware                      string
	cpus                          int
	memoryMiB                     int
	name                          string
	baseTemplate                  string
	workDir                       string
	keepImage                     bool
	diskFormat                    string
	factoryURL                    string
	timeout                       time.Duration
	verify                        bool
	verifyTimeout                 time.Duration
	output                        string
	debug                         bool
	pruneKeep                     int
	pruneDryRun                   bool
}

func main() {
	var o options
	envList(&o.srs, "XO_SR_UUID")
	envList(&o.networks, "XO_NETWORK_UUID")
	envList(&o.verifyNetworks, "XO_VERIFY_NETWORK_UUID")
	flag.Var(&o.srs, "sr", "SR UUID to store the disk on, one per pool; repeat it (or give a comma-separated list) to build in several pools (env XO_SR_UUID)")
	flag.Var(&o.networks, "network", "network UUID for a VIF on the template, at most one per pool; default: no VIF, CAPV adds its own (env XO_NETWORK_UUID)")
	flag.StringVar(&o.talosVersion, "talos-version", "", "Talos version: latest (latest stable), a minor line like v1.13 (its latest patch) or an exact version like v1.14.5")
	flag.StringVar(&o.schematicFile, "schematic", "", "Image Factory schematic YAML file (extensions, extraKernelArgs, meta, ...)")
	flag.StringVar(&o.extensions, "extensions", "", "comma-separated official system extensions to add ("+requiredExtension+" is always added)")
	flag.StringVar(&o.arch, "arch", "amd64", "image architecture")
	flag.StringVar(&o.firmware, "firmware", "bios", "VM boot firmware: bios or uefi")
	flag.IntVar(&o.cpus, "cpus", 2, "vCPUs of the template")
	flag.IntVar(&o.memoryMiB, "memory", 2048, "memory of the template, in MiB")
	flag.StringVar(&o.name, "name", "", "template name (default: talos-<version>-nocloud-<arch>-<schematic prefix>)")
	flag.StringVar(&o.baseTemplate, "base-template", "Other install media", "diskless XO template the VM is created from")
	flag.StringVar(&o.workDir, "work-dir", os.TempDir(), "directory for the downloaded image")
	flag.StringVar(&o.diskFormat, "disk-format", "vhd", "upload format: vhd (sends only the blocks holding data) or raw")
	flag.BoolVar(&o.keepImage, "keep-image", false, "keep the downloaded (compressed) image in --work-dir; later runs reuse it")
	flag.StringVar(&o.factoryURL, "factory-url", factory.DefaultURL, "Talos Image Factory URL")
	flag.DurationVar(&o.timeout, "timeout", envDuration("XOA_TIMEOUT", 30*time.Minute), "timeout of each Xen Orchestra request, including the disk upload (env XOA_TIMEOUT)")
	flag.BoolVar(&o.verify, "verify", false, "boot a clone of each template, check xen-guest-agent and the Talos API, then delete the clone")
	flag.Var(&o.verifyNetworks, "verify-network", "network UUID for the --verify test VM, one per pool; the tool must reach its IP on port 50000 (env XO_VERIFY_NETWORK_UUID)")
	flag.DurationVar(&o.verifyTimeout, "verify-timeout", 10*time.Minute, "how long --verify waits for each test VM")
	flag.StringVar(&o.output, "output", "text", "stdout format: text (template UUIDs), json, or env (shell exports of XO_TEMPLATE_UUID, XO_POOL_UUID, TALOS_VERSION)")
	flag.IntVar(&o.pruneKeep, "prune-keep", 0, "after the build, delete older templates of the same schematic, platform and arch built by this tool, keeping this many Talos versions (0: never delete)")
	flag.BoolVar(&o.pruneDryRun, "prune-dry-run", false, "with --prune-keep, only list the templates that would be deleted")
	flag.BoolVar(&o.debug, "debug", false, "print the Xen Orchestra SDK logs on stderr")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), `Usage: xo-talos --sr <sr-uuid> [--sr <sr-uuid>...] [flags]

Builds Xen Orchestra templates from the Talos Image Factory nocloud image,
one per pool. Xen Orchestra credentials come from XOA_ENDPOINT, XOA_TOKEN
(or XOA_USERNAME and XOA_PASSWORD) and XOA_INSECURE.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("xo-talos", version)
		return
	}
	if !slices.Contains(outputFormats, o.output) {
		fmt.Fprintf(os.Stderr, "error: --output must be one of %s\n", strings.Join(outputFormats, ", "))
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Print whatever succeeded even if another pool failed.
	results, err := run(ctx, o)
	if len(results) > 0 {
		if werr := writeResults(os.Stdout, o.output, results); werr != nil {
			err = errors.Join(err, werr)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", cleanError(err))
		os.Exit(1)
	}
}

// envList seeds a list flag from a comma-separated environment variable.
func envList(l *uuidList, key string) {
	if v := os.Getenv(key); v != "" {
		if err := l.Set(v); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", key, err)
			os.Exit(2)
		}
	}
}

// run builds (or finds) the template of every pool. It returns the results of
// the pools that succeeded, and an error joining the failures.
func run(ctx context.Context, o options) ([]result, error) {
	if len(o.srs) == 0 {
		return nil, errors.New("--sr is required")
	}
	if o.output == "env" && len(o.srs) > 1 {
		return nil, errors.New("--output env needs a single --sr; use --output json for several pools")
	}
	if o.diskFormat != "vhd" && o.diskFormat != "raw" {
		return nil, fmt.Errorf("--disk-format must be vhd or raw, got %q", o.diskFormat)
	}
	if o.firmware != "bios" && o.firmware != "uefi" {
		return nil, fmt.Errorf("--firmware must be bios or uefi, got %q", o.firmware)
	}
	if o.pruneKeep < 0 {
		return nil, fmt.Errorf("--prune-keep must be positive, got %d", o.pruneKeep)
	}
	if o.pruneKeep > 0 && o.name != "" {
		return nil, errors.New("--prune-keep only knows the default template names; drop --name")
	}
	if o.pruneDryRun && o.pruneKeep == 0 {
		return nil, errors.New("--prune-dry-run needs --prune-keep")
	}
	if o.name != "" && len(o.srs) > 1 {
		logf("note: --name gives the same name to the template of every pool")
	}
	schematic, err := loadSchematic(o.schematicFile, splitList(o.extensions))
	if err != nil {
		return nil, err
	}
	extensions, err := schematic.Extensions()
	if err != nil {
		return nil, err
	}

	reporter := progress.New(os.Stderr)
	fc := factory.New(o.factoryURL)
	fc.Progress = reporter
	talosVersion, err := fc.ResolveVersion(ctx, o.talosVersion)
	if err != nil {
		return nil, err
	}
	logf("Talos version: %s", talosVersion)
	if err := fc.CheckExtensions(ctx, talosVersion, extensions); err != nil {
		return nil, err
	}
	schematicID, err := fc.CreateSchematic(ctx, schematic)
	if err != nil {
		return nil, err
	}
	logf("schematic: %s (%s)", schematicID, strings.Join(extensions, ", "))

	name := o.name
	if name == "" {
		name = fmt.Sprintf("talos-%s-%s-%s-%s", talosVersion, platform, o.arch, schematicID[:8])
	}

	b, err := newBuilder(o.timeout, o.debug)
	if err != nil {
		return nil, err
	}
	b.Progress = reporter
	targets, err := resolveTargets(ctx, b, o)
	if err != nil {
		return nil, err
	}

	// Look for existing templates first: the image is only downloaded when a
	// pool needs it.
	results := make([]result, len(targets))
	var toBuild []int
	for i, t := range targets {
		results[i] = result{
			TemplateName: name,
			PoolUUID:     t.pool.String(),
			SRUUID:       t.sr.String(),
			TalosVersion: talosVersion,
			SchematicID:  schematicID,
		}
		existing, err := b.FindTemplate(ctx, t.pool, name)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			logf("pool %s: template %q already exists, nothing to build", t.pool, name)
			results[i].TemplateUUID = existing.UUID.String()
		} else {
			toBuild = append(toBuild, i)
		}
	}

	var disk *image.Disk
	if len(toBuild) > 0 {
		logf("downloading %s-%s image for Talos %s", platform, o.arch, talosVersion)
		imagePath, cached, err := fc.Download(ctx, schematicID, talosVersion, platform, o.arch, o.workDir)
		if err != nil {
			return nil, err
		}
		if cached {
			logf("reusing %s", imagePath)
		}
		if !o.keepImage {
			defer func() { _ = os.Remove(imagePath) }()
		}
		if disk, err = image.FromCompressedFile(imagePath, payloads.VDIFormat(o.diskFormat), reporter); err != nil {
			return nil, err
		}
		used, total := disk.UsedBlocks()
		logf("disk: %s, %d of %d blocks hold data", progress.Bytes(disk.VirtualSize()), used, total)
	}

	spec := xo.Spec{
		Name: name,
		Description: fmt.Sprintf("Talos %s %s-%s, schematic %s (%s). Clone it, never boot it.",
			talosVersion, platform, o.arch, schematicID, strings.Join(extensions, ", ")),
		Tags: []string{
			"talos",
			"talos-version=" + talosVersion,
			"talos-platform=" + platform,
			"talos-arch=" + o.arch,
			"talos-schematic=" + schematicID[:12],
		},
		BaseTemplate: o.baseTemplate,
		Firmware:     o.firmware,
		CPUs:         o.cpus,
		MemoryBytes:  o.memoryMiB << 20,
	}

	var ok []result
	var errs []error
	for i, t := range targets {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := processTarget(ctx, b, o, t, spec, disk, &results[i], slices.Contains(toBuild, i)); err != nil {
			logf("pool %s: %v", t.pool, err)
			errs = append(errs, fmt.Errorf("pool %s: %w", t.pool, err))
			continue
		}
		if o.pruneKeep > 0 {
			pruned, err := b.Prune(ctx, xo.PruneSpec{
				PoolID:      t.pool,
				Platform:    platform,
				Arch:        o.arch,
				SchematicID: schematicID,
				Keep:        o.pruneKeep,
				Current:     uuid.FromStringOrNil(results[i].TemplateUUID),
				DryRun:      o.pruneDryRun,
			})
			for _, p := range pruned {
				results[i].Pruned = append(results[i].Pruned, p.UUID.String())
			}
			if err != nil {
				logf("pool %s: %v", t.pool, err)
				errs = append(errs, fmt.Errorf("pool %s: %w", t.pool, err))
			}
		}
		ok = append(ok, results[i])
	}
	return ok, errors.Join(errs...)
}

// resolveTargets looks up the pool of every SR and network.
func resolveTargets(ctx context.Context, b *xo.Builder, o options) ([]target, error) {
	srPool := map[uuid.UUID]uuid.UUID{}
	for _, sr := range o.srs {
		pool, err := b.PoolOfSR(ctx, sr)
		if err != nil {
			return nil, err
		}
		srPool[sr] = pool
	}
	netPool := map[uuid.UUID]uuid.UUID{}
	for _, n := range append(slices.Clone(o.networks), o.verifyNetworks...) {
		pool, err := b.PoolOfNetwork(ctx, n)
		if err != nil {
			return nil, err
		}
		netPool[n] = pool
	}
	return planTargets(o.srs, srPool, o.networks, o.verifyNetworks, netPool, o.verify)
}

// processTarget builds the template of one pool if needed, then verifies it.
func processTarget(ctx context.Context, b *xo.Builder, o options, t target, spec xo.Spec, disk *image.Disk, res *result, build bool) error {
	if build {
		spec.PoolID, spec.SRID, spec.NetworkID, spec.Disk = t.pool, t.sr, t.network, disk
		id, err := b.Build(ctx, spec)
		if err != nil {
			return err
		}
		logf("pool %s: template %q created: %s", t.pool, spec.Name, id)
		res.TemplateUUID, res.Created = id.String(), true
	}
	if !o.verify {
		return nil
	}
	id := uuid.FromStringOrNil(res.TemplateUUID)
	err := b.Verify(ctx, xo.VerifySpec{
		TemplateID: id,
		PoolID:     t.pool,
		NetworkID:  t.verifyNetwork,
		Name:       "xo-talos-verify-" + res.TemplateUUID[:8],
		Timeout:    o.verifyTimeout,
	})
	if err != nil {
		if res.Created {
			// A template that does not boot must not be picked up by CAPV,
			// nor be found by the next run as "already exists".
			logf("deleting template %s, which failed verification", id)
			if delErr := b.DeleteTemplate(context.Background(), id); delErr != nil {
				logf("delete template %s: %v", id, delErr)
			}
		}
		return err
	}
	res.Verified = true
	return nil
}

// loadSchematic reads the schematic file, if any, and adds the required and
// requested official extensions to it.
func loadSchematic(path string, extensions []string) (factory.Schematic, error) {
	var data []byte
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	s, err := factory.ParseSchematic(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Has("embeddedMachineConfiguration") {
		logf("warning: the schematic embeds a machine configuration; CAPV provides one through cloud-init and the two may conflict")
	}
	if err := s.AddExtensions(append([]string{requiredExtension}, extensions...)...); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// newBuilder connects to Xen Orchestra with the same environment variables as
// xo-gocli.
func newBuilder(timeout time.Duration, debug bool) (*xo.Builder, error) {
	endpoint := os.Getenv("XOA_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("XOA_URL")
	}
	if endpoint == "" {
		return nil, errors.New("XOA_ENDPOINT is not set")
	}
	username := os.Getenv("XOA_USERNAME")
	if username == "" {
		username = os.Getenv("XOA_USER")
	}
	insecure := strings.ToLower(os.Getenv("XOA_INSECURE"))
	// The SDK logs every failed call as JSON, duplicating the errors this
	// tool reports; keep its logs for --debug.
	sdkLogs := []string{os.DevNull}
	if debug {
		sdkLogs = []string{"stderr"}
	}

	cfg, err := config.NewWithValues(&config.Config{
		Url:                 endpoint,
		Token:               os.Getenv("XOA_TOKEN"),
		Username:            username,
		Password:            os.Getenv("XOA_PASSWORD"),
		InsecureSkipVerify:  insecure == "1" || insecure == "true" || insecure == "yes",
		ClientTimeout:       timeout,
		Development:         debug,
		LogOutputPaths:      sdkLogs,
		LogErrorOutputPaths: sdkLogs,
	})
	if err != nil {
		return nil, err
	}
	lib, err := xov2.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to Xen Orchestra: %w", err)
	}
	rest, err := client.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to Xen Orchestra: %w", err)
	}
	return xo.NewBuilder(xo.NewSDKAPI(lib, rest), logf), nil
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}

// logf prints a progress message on one line; errors from the SDK may
// otherwise span several.
func logf(format string, args ...any) {
	fmt.Fprintln(os.Stderr, cleanLine(fmt.Sprintf(format, args...)))
}
